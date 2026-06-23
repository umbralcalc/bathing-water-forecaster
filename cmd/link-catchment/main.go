// Command link-catchment links a bathing-water sampling point to its nearest
// rainfall gauge and sanity-checks the rain→count association across the site's
// sample history — the first test of the causal backbone
// before the rainfall-driven model is committed to.
//
//	link-catchment -point 03600 -dist 15 -window 2
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
	"github.com/umbralcalc/bathing-water-forecaster/internal/hydro"
)

func main() {
	point := flag.String("point", "03600", "sampling point notation")
	dist := flag.Float64("dist", 15, "search radius for rain gauges (km)")
	window := flag.Int("window", 2, "antecedent rainfall window (days, inclusive of sample day)")
	cut := flag.Float64("cut", 500, "E. coli count treated as 'elevated' for the wet/dry split")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	bw := bwq.New()
	hy := hydro.New()

	// 1. Site location — the compliance feed is the handiest source of coordinates.
	comp, err := bw.Compliance(ctx, bwq.RegimeRBWD, *point)
	if err != nil {
		log.Fatalf("compliance: %v", err)
	}
	if len(comp) == 0 || comp[0].Lat == 0 {
		log.Fatalf("no coordinates for point %s", *point)
	}
	lat, long, name := comp[0].Lat, comp[0].Long, comp[0].BathingWaterName

	// 2. Sample history.
	samples, err := bw.InSeasonSamples(ctx, *point)
	if err != nil {
		log.Fatalf("samples: %v", err)
	}
	if len(samples) == 0 {
		log.Fatalf("no samples for point %s", *point)
	}

	// 3. Link the nearest rain gauge.
	stations, err := hy.NearbyStations(ctx, "rainfall", lat, long, *dist)
	if err != nil {
		log.Fatalf("nearby stations: %v", err)
	}
	gauges := catchment.LinkRainGauges(stations, lat, long)
	if len(gauges) == 0 {
		log.Fatalf("no daily-rainfall gauge within %g km of %s", *dist, name)
	}
	g := gauges[0]

	fmt.Printf("Site %s — %s  (%.4f, %.4f)\n", *point, name, lat, long)
	fmt.Printf("Nearest rain gauge: %s  %.1f km away\n", g.Station.Label, g.DistanceKm)
	fmt.Printf("Samples: %d (%s … %s)\n\n",
		len(samples),
		samples[len(samples)-1].Time.Format("2006-01-02"),
		samples[0].Time.Format("2006-01-02"))

	// 4. Daily rainfall over the sample span.
	oldest := samples[len(samples)-1].Time.AddDate(0, 0, -*window)
	newest := samples[0].Time
	readings, err := hy.Readings(ctx, g.DailyMeasureID, oldest, newest)
	if err != nil {
		log.Fatalf("readings: %v", err)
	}

	// 5. Pair each sample with antecedent rainfall and assess the association.
	type pair struct {
		t       time.Time
		ecoli   bwq.Count
		rain    float64
		covered bool
	}
	var pairs []pair
	for _, s := range samples {
		total, cov := catchment.AntecedentRainfall(readings, s.Time, *window)
		pairs = append(pairs, pair{t: s.Time, ecoli: s.EColi, rain: total, covered: cov == *window})
	}

	// (a) Pearson correlation of antecedent rain vs log10(count) over uncensored
	// counts with full rainfall coverage — the cleanest read of the association.
	var xs, ys []float64
	for _, p := range pairs {
		if !p.covered || !p.ecoli.Present || p.ecoli.Censoring != bwq.Actual || p.ecoli.Value <= 0 {
			continue
		}
		xs = append(xs, p.rain)
		ys = append(ys, math.Log10(p.ecoli.Value))
	}
	fmt.Printf("Association (window = %d day(s), gauge readings available for the span):\n", *window)
	if len(xs) >= 3 {
		fmt.Printf("  Pearson r(antecedent mm, log10 E.coli) = %+.3f over %d uncensored samples\n", pearson(xs, ys), len(xs))
	} else {
		fmt.Printf("  too few uncensored, covered samples (%d) for a correlation\n", len(xs))
	}

	// (b) Wet/dry exceedance split — public-legible version of the same signal.
	var wetN, wetHigh, dryN, dryHigh int
	for _, p := range pairs {
		if !p.covered || !p.ecoli.Present {
			continue
		}
		elevated := p.ecoli.Value >= *cut && p.ecoli.Censoring != bwq.LessThan
		if p.rain >= 5 {
			wetN++
			if elevated {
				wetHigh++
			}
		} else {
			dryN++
			if elevated {
				dryHigh++
			}
		}
	}
	fmt.Printf("  Elevated (E.coli ≥ %.0f) rate:  wet days (≥5mm) %s   dry days (<5mm) %s\n",
		*cut, rate(wetHigh, wetN), rate(dryHigh, dryN))

	// (c) Eyeball the highest counts and their antecedent rain.
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].ecoli.Value > pairs[j].ecoli.Value })
	fmt.Println("\nHighest counts and their antecedent rainfall:")
	for _, p := range pairs[:min(6, len(pairs))] {
		cov := ""
		if !p.covered {
			cov = "  (rain coverage incomplete)"
		}
		fmt.Printf("  %s  E.coli %s%-6.0f  antecedent %5.1f mm%s\n",
			p.t.Format("2006-01-02"), p.ecoli.Censoring, p.ecoli.Value, p.rain, cov)
	}
}

func rate(k, n int) string {
	if n == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d/%d (%.0f%%)", k, n, 100*float64(k)/float64(n))
}

func pearson(x, y []float64) float64 {
	n := float64(len(x))
	var sx, sy float64
	for i := range x {
		sx += x[i]
		sy += y[i]
	}
	mx, my := sx/n, sy/n
	var cov, vx, vy float64
	for i := range x {
		dx, dy := x[i]-mx, y[i]-my
		cov += dx * dy
		vx += dx * dx
		vy += dy * dy
	}
	if vx == 0 || vy == 0 {
		return 0
	}
	return cov / math.Sqrt(vx*vy)
}
