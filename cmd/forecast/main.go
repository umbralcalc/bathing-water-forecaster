// Command forecast commits a calibrated P(exceedance) for every site in a region
// before its next weekly sample, writing an immutable ledger to data/predictions/.
//
//	forecast -region northumberland -points 04700,04800,04600 -as-of 2025-07-01
//
// Everything used to form a commitment — training samples and the antecedent-
// rainfall covariate — is bounded strictly before the commit time, so a forecast
// can never see its own outcome. With -as-of in the past this reproduces a
// historical commitment for backtesting the loop; with the default (now) it is a
// live in-season forecast.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/umbralcalc/bathing-water-forecaster/internal/bwq"
	"github.com/umbralcalc/bathing-water-forecaster/internal/exceedance"
	"github.com/umbralcalc/bathing-water-forecaster/internal/forecast"
	"github.com/umbralcalc/bathing-water-forecaster/internal/hydro"
	"github.com/umbralcalc/bathing-water-forecaster/internal/siteload"
)

func main() {
	region := flag.String("region", "demo", "region label for the ledger filename")
	pointsCSV := flag.String("points", "04700,04800,04600", "comma-separated sampling point notations (ignored when -all)")
	all := flag.Bool("all", false, "forecast every designated England site (overrides -points)")
	limit := flag.Int("limit", 0, "cap the number of sites when -all (0 = no cap)")
	asOf := flag.String("as-of", "", "commit date YYYY-MM-DD (default: today)")
	dist := flag.Float64("dist", 15, "rain-gauge search radius (km)")
	window := flag.Int("window", 2, "antecedent rainfall window (days)")
	threshold := flag.Float64("threshold", 500, "E. coli exceedance cut (count/100ml)")
	dir := flag.String("dir", "data/predictions", "ledger output directory")
	flag.Parse()

	commitTime := time.Now().UTC()
	if *asOf != "" {
		t, err := time.Parse("2006-01-02", *asOf)
		if err != nil {
			log.Fatalf("bad -as-of: %v", err)
		}
		commitTime = t
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	bw := bwq.New()
	hy := hydro.New()

	targets, err := resolveTargets(ctx, bw, *all, *limit, *pointsCSV)
	if err != nil {
		log.Fatalf("resolve targets: %v", err)
	}
	log.Printf("forecasting %d site(s) for %s as of %s", len(targets), *region, commitTime.Format("2006-01-02"))

	commit := forecast.Commit{Region: *region, CommitTime: commitTime}
	for _, tgt := range targets {
		site, err := siteload.Load(ctx, bw, hy, tgt.Notation, tgt.Lat, tgt.Long, tgt.Name, *dist, *window)
		if err != nil {
			log.Printf("skip %s: %v", tgt.Notation, err)
			continue
		}
		point := tgt.Notation

		// Leakage-free training: samples and covariates strictly before commit.
		train := site.TrainingSet(commitTime)
		if len(train) < 20 {
			log.Printf("skip %s (%s): only %d training samples before %s",
				point, site.Name, len(train), commitTime.Format("2006-01-02"))
			continue
		}
		rain, ok := site.AntecedentAsOf(commitTime)
		if !ok {
			log.Printf("skip %s (%s): no rainfall coverage for the commit window", point, site.Name)
			continue
		}

		fit := exceedance.FitRegression(train, 1)
		p := fit.ExceedanceProb([]float64{rain}, *threshold)

		commit.Predictions = append(commit.Predictions, forecast.Prediction{
			Point:            point,
			Name:             site.Name,
			CommitTime:       commitTime,
			Determinand:      "E.coli",
			Threshold:        *threshold,
			PExceed:          p,
			WindowDays:       *window,
			AntecedentRainMM: rain,
			Gauge:            site.Gauge,
			TrainN:           len(train),
			Model:            forecast.ModelParams{Beta: fit.Beta, Sigma: fit.Sigma},
		})
		fmt.Printf("  %s  %-28s  rain %5.1fmm  P(>%.0f) = %5.1f%%  (n=%d)\n",
			point, trunc(site.Name, 28), rain, *threshold, 100*p, len(train))
	}

	if len(commit.Predictions) == 0 {
		log.Fatal("no sites could be committed")
	}
	path, err := forecast.WriteCommit(*dir, commit)
	if err != nil {
		log.Fatalf("write ledger: %v", err)
	}
	fmt.Printf("\nCommitted %d sites for %s at %s\n  → %s\n",
		len(commit.Predictions), *region, commitTime.Format("2006-01-02"), path)
}

// resolveTargets produces the sites to forecast: every designated site (with
// coordinates already attached) when all is set, otherwise the explicit -points
// list (coordinates filled in later by siteload).
func resolveTargets(ctx context.Context, bw *bwq.Client, all bool, limit int, pointsCSV string) ([]bwq.SamplingPoint, error) {
	if all {
		sites, err := bw.DesignatedSites(ctx)
		if err != nil {
			return nil, err
		}
		if limit > 0 && limit < len(sites) {
			sites = sites[:limit]
		}
		return sites, nil
	}
	var out []bwq.SamplingPoint
	for _, p := range splitCSV(pointsCSV) {
		out = append(out, bwq.SamplingPoint{Notation: p})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no -points given and -all not set")
	}
	return out, nil
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
