// Command exceedance-backtest runs an expanding-window, no-leakage backtest of
// the rainfall-driven exceedance model and scores it against the two baselines
// that set the bar: a per-site seasonal base rate, and the naive "did it
// rain recently" rule.
//
//	exceedance-backtest -points 04700,04600,03600 -window 2 -threshold 500
//	exceedance-backtest -all -limit 50
//
// For each sample in time order, the model is refitted on that site's strictly
// earlier samples (the expanding window), the antecedent rainfall up to the
// sample is used as the covariate, and the committed probability is scored
// against the sample's own censoring-resolved outcome. The same eligible samples
// score all three methods, so the comparison is apples-to-apples.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/umbralcalc/bathing-water-forecaster/internal/bwq"
	"github.com/umbralcalc/bathing-water-forecaster/internal/catchment"
	"github.com/umbralcalc/bathing-water-forecaster/internal/exceedance"
	"github.com/umbralcalc/bathing-water-forecaster/internal/forecast"
	"github.com/umbralcalc/bathing-water-forecaster/internal/hydro"
	"github.com/umbralcalc/bathing-water-forecaster/internal/siteload"
)

// row is one sample reduced to what the backtest needs.
type row struct {
	t           time.Time
	logValue    float64
	censoring   bwq.Censoring
	rain        float64
	rainOK      bool
	exceeded    bool
	determinate bool
}

func main() {
	pointsCSV := flag.String("points", "04700,04600,04800,03600,10500", "comma-separated sampling points (ignored when -all)")
	all := flag.Bool("all", false, "backtest every designated England site")
	limit := flag.Int("limit", 0, "cap sites when -all (0 = no cap)")
	dist := flag.Float64("dist", 15, "rain-gauge search radius (km)")
	window := flag.Int("window", 2, "antecedent rainfall window (days)")
	threshold := flag.Float64("threshold", 500, "E. coli exceedance cut (count/100ml)")
	minTrain := flag.Int("min-train", 30, "minimum prior samples before a site starts scoring")
	wetCut := flag.Float64("wet-cut", 5, "antecedent mm dividing the rain rule's wet/dry buckets")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	bw := bwq.New()
	hy := hydro.New()

	targets, err := resolveTargets(ctx, bw, *all, *limit, *pointsCSV)
	if err != nil {
		log.Fatalf("resolve targets: %v", err)
	}
	log.Printf("backtesting %d site(s)", len(targets))

	var model, base, rain []forecast.Resolution // pooled across sites
	for _, tgt := range targets {
		site, err := siteload.Load(ctx, bw, hy, tgt.Notation, tgt.Lat, tgt.Long, tgt.Name, *dist, *window)
		if err != nil {
			log.Printf("skip %s: %v", tgt.Notation, err)
			continue
		}
		m, b, r := backtestSite(site, *threshold, *minTrain, *wetCut)
		if len(m) == 0 {
			continue
		}
		model = append(model, m...)
		base = append(base, b...)
		rain = append(rain, r...)
		log.Printf("  %-26s scored %d samples", trunc(site.Name, 26), len(m))
	}

	if len(model) == 0 {
		log.Fatal("no samples scored — try more sites or a lower -min-train")
	}
	report(*threshold, model, base, rain)
}

// backtestSite runs the expanding-window backtest for one site, returning the
// per-sample resolutions for the model and the two baselines over the identical
// set of eligible samples.
func backtestSite(site forecast.Site, threshold float64, minTrain int, wetCut float64) (model, base, rain []forecast.Resolution) {
	// Reduce samples to time-ordered rows with covariate and outcome.
	rows := make([]row, 0, len(site.Samples))
	for _, s := range site.Samples {
		o, ok := exceedance.ObservationFromCount(s.EColi)
		if !ok {
			continue
		}
		rmm, cov := catchment.AntecedentRainfall(site.Rain, s.Time, site.WindowDays)
		exc, det := forecast.Exceeded(s.EColi, threshold)
		rows = append(rows, row{
			t: s.Time, logValue: o.LogValue, censoring: o.Censoring,
			rain: rmm, rainOK: cov == site.WindowDays,
			exceeded: exc, determinate: det,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].t.Before(rows[j].t) })

	var train []exceedance.CovObservation
	var detN, detExc, wetN, wetExc, dryN, dryExc int

	for _, cur := range rows {
		// Evaluate the target using only priors, before folding it in.
		if cur.rainOK && cur.determinate && len(train) >= minTrain && detN > 0 {
			fit := exceedance.FitRegression(train, 1)
			pModel := fit.ExceedanceProb([]float64{cur.rain}, threshold)
			pBase := float64(detExc) / float64(detN)
			pRain := rainRuleProb(cur.rain, wetCut, wetN, wetExc, dryN, dryExc, pBase)

			model = append(model, resolution(pModel, cur.exceeded))
			base = append(base, resolution(pBase, cur.exceeded))
			rain = append(rain, resolution(pRain, cur.exceeded))
		}

		// Fold the sample into the priors for subsequent targets.
		if cur.rainOK {
			train = append(train, exceedance.CovObservation{
				LogValue: cur.logValue, Censoring: cur.censoring, Covars: []float64{cur.rain},
			})
		}
		if cur.rainOK && cur.determinate {
			detN++
			if cur.exceeded {
				detExc++
			}
			if cur.rain >= wetCut {
				wetN++
				if cur.exceeded {
					wetExc++
				}
			} else {
				dryN++
				if cur.exceeded {
					dryExc++
				}
			}
		}
	}
	return model, base, rain
}

// rainRuleProb is the "did it rain" heuristic as a probability: the prior
// exceedance rate in the target's wet/dry bucket, falling back to the overall
// base rate when that bucket has no prior samples.
func rainRuleProb(rain, wetCut float64, wetN, wetExc, dryN, dryExc int, fallback float64) float64 {
	if rain >= wetCut {
		if wetN == 0 {
			return fallback
		}
		return float64(wetExc) / float64(wetN)
	}
	if dryN == 0 {
		return fallback
	}
	return float64(dryExc) / float64(dryN)
}

func resolution(p float64, exceeded bool) forecast.Resolution {
	return forecast.Resolution{
		Prediction: forecast.Prediction{PExceed: p},
		Exceeded:   exceeded,
		Brier:      forecast.Brier(p, exceeded),
		LogLoss:    forecast.LogLoss(p, exceeded),
	}
}

func report(threshold float64, model, base, rain []forecast.Resolution) {
	sm := forecast.Aggregate(model)
	sb := forecast.Aggregate(base)
	sr := forecast.Aggregate(rain)

	fmt.Printf("\nExpanding-window backtest — %d samples, %.1f%% exceeded E.coli %.0f\n",
		sm.N, 100*sm.BaseRate, threshold)
	fmt.Printf("\n  %-12s %8s %9s\n", "method", "Brier", "logloss")
	fmt.Printf("  %-12s %8.4f %9.4f\n", "rain-model", sm.MeanBrier, sm.MeanLogLoss)
	fmt.Printf("  %-12s %8.4f %9.4f\n", "base-rate", sb.MeanBrier, sb.MeanLogLoss)
	fmt.Printf("  %-12s %8.4f %9.4f\n", "rain-rule", sr.MeanBrier, sr.MeanLogLoss)

	fmt.Printf("\n  Brier skill of rain-model vs base-rate: %+.3f\n", skill(sm.MeanBrier, sb.MeanBrier))
	fmt.Printf("  Brier skill of rain-model vs rain-rule: %+.3f\n", skill(sm.MeanBrier, sr.MeanBrier))

	fmt.Println("\n  Model reliability (forecast band → empirical exceedance rate):")
	for _, b := range sm.ReliabilityBins {
		if b.N == 0 {
			continue
		}
		fmt.Printf("    %3.0f–%3.0f%%  mean %4.1f%%  →  %4.1f%%  (n=%d)\n",
			100*b.Lo, 100*b.Hi, 100*b.MeanP, 100*b.Empirical, b.N)
	}
}

// skill is the Brier skill score of a forecast against a reference: 1 means
// perfect, 0 means no better than the reference, negative means worse.
func skill(brier, ref float64) float64 {
	if ref == 0 {
		return 0
	}
	return 1 - brier/ref
}

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
	for _, p := range strings.Split(pointsCSV, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, bwq.SamplingPoint{Notation: p})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no -points given and -all not set")
	}
	return out, nil
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
