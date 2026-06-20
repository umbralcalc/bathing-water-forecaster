// Command explain demonstrates the attribution layer: for a bathing water it
// fits the rain + season exceedance model and, for illustrative samples, breaks
// each P(exceedance) forecast into a baseline plus the push from antecedent rain
// and from season — the "why" behind a number, computed exactly through the
// nonlinear link by Shapley attribution (the parts sum to the forecast).
//
// This is the explainability proof-of-concept that the stochadex partition
// composition would later generalise: here the components are columns of one
// regression; there they become separately-simulated processes (baseline,
// season, rainfall, a shared regional anomaly), each independently attributable.
//
//	explain -point 04700
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"sort"
	"time"

	"github.com/umbralcalc/bathing-water-forecaster/internal/bwq"
	"github.com/umbralcalc/bathing-water-forecaster/internal/catchment"
	"github.com/umbralcalc/bathing-water-forecaster/internal/exceedance"
	"github.com/umbralcalc/bathing-water-forecaster/internal/forecast"
	"github.com/umbralcalc/bathing-water-forecaster/internal/hydro"
	"github.com/umbralcalc/bathing-water-forecaster/internal/siteload"
)

func main() {
	point := flag.String("point", "04700", "sampling point notation")
	dist := flag.Float64("dist", 15, "rain-gauge search radius (km)")
	window := flag.Int("window", 2, "antecedent rainfall window (days)")
	threshold := flag.Float64("threshold", 500, "E. coli exceedance cut (count/100ml)")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	site, err := siteload.Load(ctx, bwq.New(), hydro.New(), *point, 0, 0, "", *dist, *window)
	if err != nil {
		log.Fatalf("load: %v", err)
	}

	// Build observations with [rain, sin(doy), cos(doy)] covariates.
	type row struct {
		t        time.Time
		rain     float64
		sin, cos float64
		exceeded bool
		det      bool
	}
	var obs []exceedance.CovObservation
	var rows []row
	for _, s := range site.Samples {
		o, ok := exceedance.ObservationFromCount(s.EColi)
		if !ok {
			continue
		}
		rmm, cov := catchment.AntecedentRainfall(site.Rain, s.Time, *window)
		if cov != *window {
			continue
		}
		ang := 2 * math.Pi * float64(s.Time.YearDay()) / 365.25
		sin, cos := math.Sin(ang), math.Cos(ang)
		exc, det := forecast.Exceeded(s.EColi, *threshold)
		obs = append(obs, exceedance.CovObservation{LogValue: o.LogValue, Censoring: o.Censoring, Covars: []float64{rmm, sin, cos}})
		rows = append(rows, row{s.Time, rmm, sin, cos, exc, det})
	}
	if len(obs) < 40 {
		log.Fatalf("only %d usable samples for %s", len(obs), *point)
	}

	fit := exceedance.FitRegression(obs, 3)
	features := []exceedance.Feature{{Name: "rain", Cols: []int{0}}, {Name: "season", Cols: []int{1, 2}}}
	neutral := []float64{0, 0, 0} // no rain, average-season day

	fmt.Printf("Site %s — %s   (gauge %s, %d-day rain)\n", *point, site.Name, site.Gauge, *window)
	fmt.Printf("Fitted: log E.coli ~ Normal(%.2f + %.4f·rain + season, %.2f)\n",
		fit.Beta[0], fit.Beta[1], fit.Sigma)
	base := fit.ExceedanceProb(neutral, *threshold)
	fmt.Printf("Dry, average-season baseline P(E.coli > %.0f) = %.1f%%\n\n", *threshold, 100*base)

	// Pick illustrative cases: wettest, a typical dry day, and the latest few.
	sort.Slice(rows, func(i, j int) bool { return rows[i].rain > rows[j].rain })
	picks := map[int]bool{0: true, 1: true} // two wettest
	picks[len(rows)/2] = true               // a median-rain day
	picks[len(rows)-1] = true               // a driest day
	idx := make([]int, 0, len(picks))
	for i := range picks {
		idx = append(idx, i)
	}
	sort.Ints(idx)

	fmt.Printf("  %-12s %6s   %9s = %8s + %7s + %7s   %s\n",
		"date", "rain", "P(exc)", "baseline", "rain", "season", "outcome")
	for _, i := range idx {
		r := rows[i]
		a := fit.AttributeExceedance([]float64{r.rain, r.sin, r.cos}, neutral, features, *threshold)
		fmt.Printf("  %-12s %5.1fmm  %8.1f%% = %7.1f%% + %+6.1f%% + %+6.1f%%   %s\n",
			r.t.Format("2006-01-02"), r.rain,
			100*a.Total, 100*a.Baseline, 100*a.Contrib["rain"], 100*a.Contrib["season"],
			outcome(r.det, r.exceeded))
	}

	fmt.Printf("\nReading: each forecast = dry baseline + the push from recent rain + the\n")
	fmt.Printf("seasonal push (− early/late season, + mid-summer). The three sum exactly to\n")
	fmt.Printf("P(exceed); rain is the transient driver, baseline the site's structural level.\n")
}

func outcome(det, exceeded bool) string {
	switch {
	case !det:
		return "(censored)"
	case exceeded:
		return "EXCEEDED"
	default:
		return "ok"
	}
}
