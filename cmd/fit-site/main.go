// Command fit-site fits the single-site rainfall-driven censored exceedance model
// for one bathing water and prints the implied rainfall→P(exceed) curve — the
// end-to-end bridge from the validated rain association to a predictive model,
// and a preview of the dashboard's rainfall→exceedance explorer.
//
//	fit-site -point 04700 -window 2 -threshold 500
//
// Unlike the link-catchment Pearson check, this uses every sample — the censored
// "< 10" readings included — through the censored likelihood.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/umbralcalc/bathing-water-forecaster/internal/bwq"
	"github.com/umbralcalc/bathing-water-forecaster/internal/catchment"
	"github.com/umbralcalc/bathing-water-forecaster/internal/exceedance"
	"github.com/umbralcalc/bathing-water-forecaster/internal/hydro"
)

func main() {
	point := flag.String("point", "04700", "sampling point notation")
	dist := flag.Float64("dist", 15, "rain-gauge search radius (km)")
	window := flag.Int("window", 2, "antecedent rainfall window (days)")
	threshold := flag.Float64("threshold", 500, "E. coli exceedance cut (count/100ml)")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	bw := bwq.New()
	hy := hydro.New()

	comp, err := bw.Compliance(ctx, bwq.RegimeRBWD, *point)
	if err != nil || len(comp) == 0 || comp[0].Lat == 0 {
		log.Fatalf("coordinates for point %s: %v", *point, err)
	}
	lat, long, name := comp[0].Lat, comp[0].Long, comp[0].BathingWaterName

	samples, err := bw.InSeasonSamples(ctx, *point)
	if err != nil {
		log.Fatalf("samples: %v", err)
	}
	stations, err := hy.NearbyStations(ctx, "rainfall", lat, long, *dist)
	if err != nil {
		log.Fatalf("stations: %v", err)
	}
	gauges := catchment.LinkRainGauges(stations, lat, long)
	if len(gauges) == 0 {
		log.Fatalf("no rain gauge within %g km", *dist)
	}
	g := gauges[0]

	oldest := samples[len(samples)-1].Time.AddDate(0, 0, -*window)
	readings, err := hy.Readings(ctx, g.DailyMeasureID, oldest, samples[0].Time)
	if err != nil {
		log.Fatalf("readings: %v", err)
	}

	// Assemble censored observations with antecedent rainfall as the covariate.
	var obs []exceedance.CovObservation
	var censoredN int
	for _, s := range samples {
		o, ok := exceedance.ObservationFromCount(s.EColi)
		if !ok {
			continue
		}
		rain, cov := catchment.AntecedentRainfall(readings, s.Time, *window)
		if cov != *window {
			continue // require complete rainfall coverage of the window
		}
		obs = append(obs, exceedance.CovObservation{
			LogValue:  o.LogValue,
			Censoring: o.Censoring,
			Covars:    []float64{rain},
		})
		if o.Censoring != bwq.Actual {
			censoredN++
		}
	}
	if len(obs) < 20 {
		log.Fatalf("only %d usable samples for %s — too few to fit", len(obs), name)
	}

	fit := exceedance.FitRegression(obs, 1)

	// Intercept-only baseline for the same observations.
	plain := make([]exceedance.Observation, len(obs))
	for i, o := range obs {
		plain[i] = exceedance.Observation{LogValue: o.LogValue, Censoring: o.Censoring}
	}
	base := exceedance.FitGaussian(plain)

	fmt.Printf("Site %s — %s\n", *point, name)
	fmt.Printf("Gauge %s (%.1f km), %d-day antecedent window\n", g.Station.Label, g.DistanceKm, *window)
	fmt.Printf("Fitted on %d samples (%d censored, used via the censored likelihood)\n\n", len(obs), censoredN)

	fmt.Printf("Model: log E.coli ~ Normal(%.3f + %.4f·rain_mm, %.3f)\n", fit.Beta[0], fit.Beta[1], fit.Sigma)
	fmt.Printf("  each mm of antecedent rain multiplies expected count by ×%.3f\n", math.Exp(fit.Beta[1]))
	fmt.Printf("  log-likelihood: rain model %.1f vs intercept-only %.1f  (Δ %+.1f)\n\n",
		fit.LogLk, base.LogLk, fit.LogLk-base.LogLk)

	fmt.Printf("Implied P(E.coli > %.0f) by antecedent rainfall:\n", *threshold)
	for _, mm := range []float64{0, 5, 10, 20, 40} {
		p := fit.ExceedanceProb([]float64{mm}, *threshold)
		fmt.Printf("  %4.0f mm  →  %5.1f%%  %s\n", mm, 100*p, bar(p))
	}
}

func bar(p float64) string {
	n := int(p*40 + 0.5)
	s := make([]byte, n)
	for i := range s {
		s[i] = '#'
	}
	return string(s)
}
