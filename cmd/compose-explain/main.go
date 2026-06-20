// Command compose-explain runs the stochadex partition composition for a bathing
// water and shows each forecast decomposed into its component partitions —
// baseline, season, rainfall — read straight out of the simulation's state
// storage. It is the stochadex counterpart of cmd/explain: the same fitted model,
// but evaluated as coupled partitions rather than one regression, demonstrating
// that the composition reproduces the model while making each component a
// first-class, separately-stored quantity.
//
//	compose-explain -point 04700
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
	"github.com/umbralcalc/bathing-water-forecaster/internal/compose"
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

	type meta struct {
		t        time.Time
		rain     float64
		exceeded bool
		det      bool
	}
	var obs []exceedance.CovObservation
	var days []compose.DayInput
	var rows []meta
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
		days = append(days, compose.DayInput{Rain: rmm, Sin: sin, Cos: cos})
		rows = append(rows, meta{s.Time, rmm, exc, det})
	}
	if len(obs) < 40 {
		log.Fatalf("only %d usable samples for %s", len(obs), *point)
	}

	fit := exceedance.FitRegression(obs, 3)
	c := compose.Coeffs{
		Beta0: fit.Beta[0], BetaRain: fit.Beta[1], BetaSin: fit.Beta[2], BetaCos: fit.Beta[3],
		Sigma: fit.Sigma, LogThreshold: math.Log(*threshold),
	}

	// Run the whole history through the stochadex partition composition once.
	d := compose.Run(c, days)

	fmt.Printf("Site %s — %s   (stochadex partition composition)\n", *point, site.Name)
	fmt.Printf("Partitions: baseline + season + rainfall → concentration → P(exceed)\n")
	fmt.Printf("Latent log E.coli mean μ is summed from the component partitions; σ=%.2f.\n\n", fit.Sigma)

	// Illustrative days: wettest, median-rain, driest.
	order := make([]int, len(rows))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return rows[order[a]].rain > rows[order[b]].rain })
	picks := []int{order[0], order[1], order[len(order)/2], order[len(order)-1]}
	sort.Ints(picks)

	fmt.Printf("  %-12s %6s   %8s %8s %8s = %6s   %8s  %s\n",
		"date", "rain", "base μ", "season", "rain μ", "μ", "P(exceed)", "outcome")
	for _, i := range picks {
		r := rows[i]
		fmt.Printf("  %-12s %5.1fmm   %8.2f %+8.2f %+8.2f = %6.2f   %7.1f%%  %s\n",
			r.t.Format("2006-01-02"), r.rain,
			d.Baseline[i], d.Season[i], d.Rainfall[i], d.Mu[i], 100*d.PExceed[i], outcome(r.det, r.exceeded))
	}

	// Confirm the composition reproduces the direct regression.
	var maxDiff float64
	for i := range days {
		got := d.PExceed[i]
		want := fit.ExceedanceProb([]float64{days[i].Rain, days[i].Sin, days[i].Cos}, *threshold)
		if dd := math.Abs(got - want); dd > maxDiff {
			maxDiff = dd
		}
	}
	fmt.Printf("\nMax |composed − regression| P(exceed) over %d days: %.2e  (the partitions reproduce the model)\n",
		len(days), maxDiff)
	fmt.Printf("Each row's μ is the sum of three separately-simulated partitions, so the\n")
	fmt.Printf("forecast carries its own breakdown — the explainability the composition adds.\n")
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
