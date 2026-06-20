// Command vs-ea-prediction scores the calibrated exceedance model head-to-head
// against the Environment Agency's own operational RiskPrediction flag, on the
// same resolved samples — the project's distinctive open comparison.
//
//	vs-ea-prediction -points 04700,04600,04800,03600 -window 2 -threshold 500
//
// The EA flag is a normal/increased triage signal, not a probability. To compare
// fairly it is turned into an out-of-sample probability the same way every other
// baseline here is: the prior exceedance rate among earlier samples carrying the
// same flag (expanding window). The model, the EA-calibrated flag and the plain
// base rate are all scored on the identical set of samples that carry both an EA
// flag and a censoring-determinate outcome.
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

type row struct {
	t           time.Time
	logValue    float64
	censoring   bwq.Censoring
	rain        float64
	rainOK      bool
	exceeded    bool
	determinate bool
	eaLevel     bwq.RiskLevel
	eaPresent   bool
}

// contingency accumulates the EA flag's raw discrimination.
type contingency struct{ incN, incExc, normN, normExc int }

func main() {
	pointsCSV := flag.String("points", "04700,04600,04800,03600,04250", "comma-separated sampling points (ignored when -all)")
	all := flag.Bool("all", false, "compare on every designated England site")
	limit := flag.Int("limit", 0, "cap sites when -all (0 = no cap)")
	dist := flag.Float64("dist", 15, "rain-gauge search radius (km)")
	window := flag.Int("window", 2, "antecedent rainfall window (days)")
	threshold := flag.Float64("threshold", 500, "E. coli exceedance cut (count/100ml)")
	minTrain := flag.Int("min-train", 30, "minimum prior samples before a site starts scoring")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	bw := bwq.New()
	hy := hydro.New()

	targets, err := resolveTargets(ctx, bw, *all, *limit, *pointsCSV)
	if err != nil {
		log.Fatalf("resolve targets: %v", err)
	}
	log.Printf("comparing %d site(s) against the EA flag", len(targets))

	var model, ea, base []forecast.Resolution
	var ctg contingency
	sitesWithEA := 0

	for _, tgt := range targets {
		site, err := siteload.Load(ctx, bw, hy, tgt.Notation, tgt.Lat, tgt.Long, tgt.Name, *dist, *window)
		if err != nil {
			log.Printf("skip %s: %v", tgt.Notation, err)
			continue
		}
		preds, err := bw.RiskPredictions(ctx, tgt.Notation)
		if err != nil {
			log.Printf("skip %s (risk preds): %v", tgt.Notation, err)
			continue
		}
		flagByDay, increasedFlags := flagIndex(preds)
		if increasedFlags == 0 {
			log.Printf("  %-26s no 'increased' EA flags — not EA-forecast, skipping", trunc(site.Name, 26))
			continue
		}
		sitesWithEA++

		m, e, b := compareSite(site, flagByDay, *threshold, *minTrain, &ctg)
		model = append(model, m...)
		ea = append(ea, e...)
		base = append(base, b...)
		log.Printf("  %-26s %d matched samples (%d EA 'increased' in history)", trunc(site.Name, 26), len(m), increasedFlags)
	}

	if len(model) == 0 {
		log.Fatal("no samples matched an EA flag — try sites with active PRF forecasts")
	}
	report(*threshold, sitesWithEA, model, ea, base, ctg)
}

// flagIndex maps each forecast day to the EA flag published latest for that day,
// and counts how many 'increased' flags the site has (its PRF-activity signal).
func flagIndex(preds []bwq.RiskPrediction) (map[string]bwq.RiskLevel, int) {
	type stamped struct {
		level bwq.RiskLevel
		pub   time.Time
	}
	best := map[string]stamped{}
	increased := 0
	for _, p := range preds {
		if p.Date.IsZero() {
			continue
		}
		if p.Level == bwq.RiskIncreased {
			increased++
		}
		key := p.Date.Format("2006-01-02")
		if cur, ok := best[key]; !ok || p.PublishedAt.After(cur.pub) {
			best[key] = stamped{p.Level, p.PublishedAt}
		}
	}
	out := make(map[string]bwq.RiskLevel, len(best))
	for k, v := range best {
		out[k] = v.level
	}
	return out, increased
}

func compareSite(site forecast.Site, flagByDay map[string]bwq.RiskLevel, threshold float64, minTrain int, ctg *contingency) (model, ea, base []forecast.Resolution) {
	rows := make([]row, 0, len(site.Samples))
	for _, s := range site.Samples {
		o, ok := exceedance.ObservationFromCount(s.EColi)
		if !ok {
			continue
		}
		rmm, cov := catchment.AntecedentRainfall(site.Rain, s.Time, site.WindowDays)
		exc, det := forecast.Exceeded(s.EColi, threshold)
		lvl, present := flagByDay[s.Time.Format("2006-01-02")]
		rows = append(rows, row{
			t: s.Time, logValue: o.LogValue, censoring: o.Censoring,
			rain: rmm, rainOK: cov == site.WindowDays,
			exceeded: exc, determinate: det,
			eaLevel: lvl, eaPresent: present && lvl != bwq.RiskUnknown,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].t.Before(rows[j].t) })

	var train []exceedance.CovObservation
	var detN, detExc, incN, incExc, normN, normExc int

	for _, cur := range rows {
		eligible := cur.determinate && cur.rainOK && cur.eaPresent && len(train) >= minTrain && detN > 0
		if eligible {
			fit := exceedance.FitRegression(train, 1)
			pModel := fit.ExceedanceProb([]float64{cur.rain}, threshold)
			pBase := float64(detExc) / float64(detN)
			pEA := eaCalibratedProb(cur.eaLevel, incN, incExc, normN, normExc, pBase)

			model = append(model, resolution(pModel, cur.exceeded))
			ea = append(ea, resolution(pEA, cur.exceeded))
			base = append(base, resolution(pBase, cur.exceeded))

			if cur.eaLevel == bwq.RiskIncreased {
				ctg.incN++
				if cur.exceeded {
					ctg.incExc++
				}
			} else {
				ctg.normN++
				if cur.exceeded {
					ctg.normExc++
				}
			}
		}

		// Fold the sample into the expanding priors.
		if cur.rainOK {
			train = append(train, exceedance.CovObservation{LogValue: cur.logValue, Censoring: cur.censoring, Covars: []float64{cur.rain}})
		}
		if cur.determinate && cur.rainOK && cur.eaPresent {
			detN++
			if cur.exceeded {
				detExc++
			}
			if cur.eaLevel == bwq.RiskIncreased {
				incN++
				if cur.exceeded {
					incExc++
				}
			} else {
				normN++
				if cur.exceeded {
					normExc++
				}
			}
		}
	}
	return model, ea, base
}

func eaCalibratedProb(level bwq.RiskLevel, incN, incExc, normN, normExc int, fallback float64) float64 {
	if level == bwq.RiskIncreased {
		if incN == 0 {
			return fallback
		}
		return float64(incExc) / float64(incN)
	}
	if normN == 0 {
		return fallback
	}
	return float64(normExc) / float64(normN)
}

func resolution(p float64, exceeded bool) forecast.Resolution {
	return forecast.Resolution{
		Prediction: forecast.Prediction{PExceed: p},
		Exceeded:   exceeded,
		Brier:      forecast.Brier(p, exceeded),
		LogLoss:    forecast.LogLoss(p, exceeded),
	}
}

func report(threshold float64, sites int, model, ea, base []forecast.Resolution, ctg contingency) {
	sm := forecast.Aggregate(model)
	se := forecast.Aggregate(ea)
	sb := forecast.Aggregate(base)

	fmt.Printf("\nHead-to-head vs EA RiskPrediction — %d EA-forecast sites, %d matched samples, %.1f%% exceeded E.coli %.0f\n",
		sites, sm.N, 100*sm.BaseRate, threshold)

	fmt.Println("\n  EA flag discrimination (raw):")
	fmt.Printf("    when EA said 'increased': %s exceeded\n", rate(ctg.incExc, ctg.incN))
	fmt.Printf("    when EA said 'normal':    %s exceeded\n", rate(ctg.normExc, ctg.normN))

	fmt.Printf("\n  %-16s %8s %9s\n", "method", "Brier", "logloss")
	fmt.Printf("  %-16s %8.4f %9.4f\n", "rain-model", sm.MeanBrier, sm.MeanLogLoss)
	fmt.Printf("  %-16s %8.4f %9.4f\n", "EA-calibrated", se.MeanBrier, se.MeanLogLoss)
	fmt.Printf("  %-16s %8.4f %9.4f\n", "base-rate", sb.MeanBrier, sb.MeanLogLoss)

	fmt.Printf("\n  Brier skill of rain-model vs EA flag:   %+.3f\n", skill(sm.MeanBrier, se.MeanBrier))
	fmt.Printf("  Brier skill of rain-model vs base-rate: %+.3f\n", skill(sm.MeanBrier, sb.MeanBrier))

	fmt.Println("\n  Model reliability (forecast band → empirical exceedance rate):")
	for _, b := range sm.ReliabilityBins {
		if b.N == 0 {
			continue
		}
		fmt.Printf("    %3.0f–%3.0f%%  mean %4.1f%%  →  %4.1f%%  (n=%d)\n", 100*b.Lo, 100*b.Hi, 100*b.MeanP, 100*b.Empirical, b.N)
	}
}

func rate(k, n int) string {
	if n == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d/%d (%.0f%%)", k, n, 100*float64(k)/float64(n))
}

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
