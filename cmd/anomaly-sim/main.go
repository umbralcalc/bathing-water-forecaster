// Command anomaly-sim demonstrates the shared regional anomaly: it fits several
// real sites in a region, then simulates a season where one Ornstein–Uhlenbeck
// "wet-week" process — built as a stochadex partition — drives every site at
// once. The output shows the anomaly trajectory and each site's exceedance
// probability rising and falling coherently with it, the behaviour the per-site
// model cannot produce.
//
// This is a forward simulation of the structure (the empirical regional effect is
// real but weak, ~0.05 excess residual correlation); calibrating the OU to the
// data by simulation-based inference is the next step.
//
//	anomaly-sim -points 04600,04700,04800 -weeks 20
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/umbralcalc/bathing-water-forecaster/internal/bwq"
	"github.com/umbralcalc/bathing-water-forecaster/internal/catchment"
	"github.com/umbralcalc/bathing-water-forecaster/internal/compose"
	"github.com/umbralcalc/bathing-water-forecaster/internal/exceedance"
	"github.com/umbralcalc/bathing-water-forecaster/internal/forecast"
	"github.com/umbralcalc/bathing-water-forecaster/internal/hydro"
	"github.com/umbralcalc/bathing-water-forecaster/internal/siteload"
)

func main() {
	pointsCSV := flag.String("points", "04600,04700,04800", "comma-separated sampling points in one region")
	weeks := flag.Int("weeks", 20, "weeks to simulate")
	theta := flag.Float64("theta", 0.4, "OU reversion speed per week")
	sigma := flag.Float64("sigma", 0.6, "OU volatility (log-count units)")
	lambda := flag.Float64("lambda", 1.0, "each site's loading on the shared anomaly")
	threshold := flag.Float64("threshold", 500, "E. coli exceedance cut")
	seed := flag.Uint64("seed", 20240807, "OU random seed")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	bw, hy := bwq.New(), hydro.New()

	var sites []compose.RegionalSite
	var names []string
	for _, p := range strings.Split(*pointsCSV, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		site, err := siteload.Load(ctx, bw, hy, p, 0, 0, "", 15, 2)
		if err != nil {
			log.Printf("skip %s: %v", p, err)
			continue
		}
		fit, ok := fitBaseline(site, *threshold)
		if !ok {
			continue
		}
		sites = append(sites, compose.RegionalSite{
			Name: p, BaseMu: fit.Beta[0], Sigma: fit.Sigma, Lambda: *lambda, LogThreshold: math.Log(*threshold),
		})
		names = append(names, fmt.Sprintf("%s/%s", p, short(site.Name)))
	}
	if len(sites) < 2 {
		log.Fatal("need at least 2 sites in the region")
	}

	run := compose.RunRegional(sites, compose.OUParams{Theta: *theta, Sigma: *sigma, Mu: 0, Init: 0, Seed: *seed}, *weeks)

	fmt.Printf("Shared regional wet-week anomaly driving %d sites (OU θ=%.2f σ=%.2f)\n\n", len(sites), *theta, *sigma)
	fmt.Printf("  %-5s %7s", "week", "anomaly")
	for _, n := range names {
		fmt.Printf("  %12s", n)
	}
	fmt.Println("   (P(exceed) per site)")
	for w := 0; w < *weeks; w++ {
		fmt.Printf("  %-5d %+7.2f", w+1, run.Anomaly[w])
		for _, s := range sites {
			fmt.Printf("  %11.1f%%", 100*run.PExceed[s.Name][w])
		}
		fmt.Println()
	}

	fmt.Printf("\nWhen the anomaly is high, every loaded site's risk rises together — one\n")
	fmt.Printf("latent process, a whole coastline. The per-site model has no such shared term.\n")
}

// fitBaseline fits the rain+season model and returns it; BaseMu for the
// simulation is its intercept (the dry, average-season baseline log-count).
func fitBaseline(site forecast.Site, threshold float64) (exceedance.Regression, bool) {
	var obs []exceedance.CovObservation
	for _, s := range site.Samples {
		o, ok := exceedance.ObservationFromCount(s.EColi)
		if !ok {
			continue
		}
		rmm, cov := catchment.AntecedentRainfall(site.Rain, s.Time, 2)
		if cov != 2 {
			continue
		}
		ang := 2 * math.Pi * float64(s.Time.YearDay()) / 365.25
		obs = append(obs, exceedance.CovObservation{LogValue: o.LogValue, Censoring: o.Censoring, Covars: []float64{rmm, math.Sin(ang), math.Cos(ang)}})
	}
	if len(obs) < 40 {
		return exceedance.Regression{}, false
	}
	return exceedance.FitRegression(obs, 3), true
}

func short(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	return s
}
