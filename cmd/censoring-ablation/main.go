// Command censoring-ablation measures, on real data, the calibration cost of
// ignoring censoring — the honest figure for the cost of the shortcut. It runs the identical
// expanding-window backtest twice: once with the censored likelihood (treating a
// "< 10" reading as the interval observation it is) and once naively (substituting
// the reporting cap as if it were an exact count). Both are scored against the
// same censoring-resolved outcomes, so the gap is purely the cost of the shortcut.
//
//	censoring-ablation -points 04700,04600,03600 -window 2 -threshold 500
//
// The synthetic TestNaiveSubstitutionIsBiased proves the direction of the bias in
// principle; this quantifies what it does to out-of-sample skill in the field.
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
}

func main() {
	pointsCSV := flag.String("points", "04700,04600,04800,03600,10500", "comma-separated sampling points (ignored when -all)")
	all := flag.Bool("all", false, "ablate over every designated England site")
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
	log.Printf("ablating censoring over %d site(s)", len(targets))

	var censored, naive, base []forecast.Resolution
	var totalObs, totalCensored int

	for _, tgt := range targets {
		site, err := siteload.Load(ctx, bw, hy, tgt.Notation, tgt.Lat, tgt.Long, tgt.Name, *dist, *window)
		if err != nil {
			log.Printf("skip %s: %v", tgt.Notation, err)
			continue
		}
		c, n, b, nObs, nCens := ablateSite(site, *threshold, *minTrain)
		if len(c) == 0 {
			continue
		}
		censored = append(censored, c...)
		naive = append(naive, n...)
		base = append(base, b...)
		totalObs += nObs
		totalCensored += nCens
		log.Printf("  %-26s %d scored, %.0f%% of its counts censored",
			trunc(site.Name, 26), len(c), 100*float64(nCens)/float64(nObs))
	}

	if len(censored) == 0 {
		log.Fatal("no samples scored")
	}
	report(*threshold, censored, naive, base, totalObs, totalCensored)
}

// ablateSite runs the paired expanding-window backtest for one site and returns
// the censored, naive and base-rate resolutions over the identical eligible
// samples, plus the observation and censored-observation counts.
func ablateSite(site forecast.Site, threshold float64, minTrain int) (censored, naive, base []forecast.Resolution, nObs, nCensored int) {
	rows := make([]row, 0, len(site.Samples))
	for _, s := range site.Samples {
		// Use only positively-reported counts so the two arms differ purely in
		// how the "<"/">" qualifier is handled, not in floor-of-zero edge cases.
		if !s.EColi.Present || s.EColi.Value <= 0 {
			continue
		}
		o, _ := exceedance.ObservationFromCount(s.EColi)
		rmm, cov := catchment.AntecedentRainfall(site.Rain, s.Time, site.WindowDays)
		exc, det := forecast.Exceeded(s.EColi, threshold)
		rows = append(rows, row{
			t: s.Time, logValue: o.LogValue, censoring: o.Censoring,
			rain: rmm, rainOK: cov == site.WindowDays,
			exceeded: exc, determinate: det,
		})
		nObs++
		if o.Censoring != bwq.Actual {
			nCensored++
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].t.Before(rows[j].t) })

	var trainC, trainN []exceedance.CovObservation
	var detN, detExc int

	for _, cur := range rows {
		if cur.rainOK && cur.determinate && len(trainC) >= minTrain && detN > 0 {
			fitC := exceedance.FitRegression(trainC, 1)
			fitN := exceedance.FitRegression(trainN, 1)
			pBase := float64(detExc) / float64(detN)

			censored = append(censored, resolution(fitC.ExceedanceProb([]float64{cur.rain}, threshold), cur.exceeded))
			naive = append(naive, resolution(fitN.ExceedanceProb([]float64{cur.rain}, threshold), cur.exceeded))
			base = append(base, resolution(pBase, cur.exceeded))
		}

		if cur.rainOK {
			// Censored arm: honour the qualifier. Naive arm: same logged cap value,
			// but treated as an exact (Actual) observation.
			trainC = append(trainC, exceedance.CovObservation{LogValue: cur.logValue, Censoring: cur.censoring, Covars: []float64{cur.rain}})
			trainN = append(trainN, exceedance.CovObservation{LogValue: cur.logValue, Censoring: bwq.Actual, Covars: []float64{cur.rain}})
		}
		if cur.rainOK && cur.determinate {
			detN++
			if cur.exceeded {
				detExc++
			}
		}
	}
	return censored, naive, base, nObs, nCensored
}

func resolution(p float64, exceeded bool) forecast.Resolution {
	return forecast.Resolution{
		Prediction: forecast.Prediction{PExceed: p},
		Exceeded:   exceeded,
		Brier:      forecast.Brier(p, exceeded),
		LogLoss:    forecast.LogLoss(p, exceeded),
	}
}

func report(threshold float64, censored, naive, base []forecast.Resolution, nObs, nCens int) {
	sc := forecast.Aggregate(censored)
	sn := forecast.Aggregate(naive)
	sb := forecast.Aggregate(base)

	fmt.Printf("\nCensoring ablation — %d samples scored, %.1f%% exceeded E.coli %.0f\n", sc.N, 100*sc.BaseRate, threshold)
	fmt.Printf("Training data was %.0f%% censored (%d of %d counts below the reporting limit)\n",
		100*float64(nCens)/float64(nObs), nCens, nObs)

	fmt.Printf("\n  %-18s %8s %9s\n", "model", "Brier", "logloss")
	fmt.Printf("  %-18s %8.4f %9.4f\n", "censored (proper)", sc.MeanBrier, sc.MeanLogLoss)
	fmt.Printf("  %-18s %8.4f %9.4f\n", "naive (substitute)", sn.MeanBrier, sn.MeanLogLoss)
	fmt.Printf("  %-18s %8.4f %9.4f\n", "base-rate", sb.MeanBrier, sb.MeanLogLoss)

	fmt.Printf("\n  Cost of ignoring censoring:\n")
	fmt.Printf("    Brier   worsens by %+.4f  (%+.1f%%)\n", sn.MeanBrier-sc.MeanBrier, pct(sn.MeanBrier, sc.MeanBrier))
	fmt.Printf("    logloss worsens by %+.4f  (%+.1f%%)\n", sn.MeanLogLoss-sc.MeanLogLoss, pct(sn.MeanLogLoss, sc.MeanLogLoss))

	fmt.Println("\n  Reliability — censored vs naive (forecast band → empirical rate):")
	cb := sc.ReliabilityBins
	nb := sn.ReliabilityBins
	for i := range cb {
		if cb[i].N == 0 && nb[i].N == 0 {
			continue
		}
		fmt.Printf("    %3.0f–%3.0f%%   censored mean %4.1f%%→%4.1f%% (n=%d)   naive mean %4.1f%%→%4.1f%% (n=%d)\n",
			100*cb[i].Lo, 100*cb[i].Hi,
			100*cb[i].MeanP, 100*cb[i].Empirical, cb[i].N,
			100*nb[i].MeanP, 100*nb[i].Empirical, nb[i].N)
	}
}

func pct(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return 100 * (a - b) / b
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
