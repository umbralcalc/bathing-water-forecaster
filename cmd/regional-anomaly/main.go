// Command regional-anomaly tests whether a shared regional "wet-week" effect
// actually exists before any latent factor is built to model it. It fits the
// per-site rain+season model, takes the standardised residuals of the uncensored
// samples, and asks: within a NUTS1 region, are residuals from the SAME calendar
// week more correlated than residuals from DIFFERENT weeks? A positive same-week
// correlation that decays across weeks is the signature of a coherent regional
// anomaly — the thing an Ornstein–Uhlenbeck partition would capture. If it is
// ~zero, the shared factor is not worth modelling.
//
//	regional-anomaly -all -limit 200
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
	"github.com/umbralcalc/bathing-water-forecaster/internal/exceedance"
	"github.com/umbralcalc/bathing-water-forecaster/internal/forecast"
	"github.com/umbralcalc/bathing-water-forecaster/internal/hydro"
	"github.com/umbralcalc/bathing-water-forecaster/internal/siteload"
)

type resid struct {
	region   string
	yearWeek int // year*100 + ISO week
	z        float64
}

func main() {
	pointsCSV := flag.String("points", "", "comma-separated points (default: -all)")
	all := flag.Bool("all", true, "use every designated England site")
	limit := flag.Int("limit", 200, "cap sites when -all")
	dist := flag.Float64("dist", 15, "rain-gauge search radius (km)")
	window := flag.Int("window", 2, "antecedent rainfall window (days)")
	flag.Parse()
	if *pointsCSV != "" {
		*all = false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()
	bw, hy := bwq.New(), hydro.New()

	targets, err := resolveTargets(ctx, bw, *all, *limit, *pointsCSV)
	if err != nil {
		log.Fatalf("targets: %v", err)
	}
	log.Printf("measuring regional anomaly over %d site(s)", len(targets))

	var residuals []resid
	for _, tgt := range targets {
		site, err := siteload.Load(ctx, bw, hy, tgt.Notation, tgt.Lat, tgt.Long, tgt.Name, *dist, *window)
		if err != nil {
			continue
		}
		region := ""
		if len(site.Samples) > 0 {
			region = bwq.RegionOf(site.Samples[0].BathingWaterID)
		}
		residuals = append(residuals, siteResiduals(site, *window, region)...)
	}
	if len(residuals) < 100 {
		log.Fatalf("only %d residuals — need more sites", len(residuals))
	}

	report(residuals)
}

// siteResiduals fits the site's rain+season model and returns residuals (in σ
// units) for its uncensored samples, tagged by region and ISO year-week.
func siteResiduals(site forecast.Site, window int, region string) []resid {
	type meta struct {
		t   time.Time
		lv  float64
		det bool
	}
	var obs []exceedance.CovObservation
	var rows []meta
	for _, s := range site.Samples {
		o, ok := exceedance.ObservationFromCount(s.EColi)
		if !ok {
			continue
		}
		rmm, cov := catchment.AntecedentRainfall(site.Rain, s.Time, window)
		if cov != window {
			continue
		}
		ang := 2 * math.Pi * float64(s.Time.YearDay()) / 365.25
		obs = append(obs, exceedance.CovObservation{LogValue: o.LogValue, Censoring: o.Censoring, Covars: []float64{rmm, math.Sin(ang), math.Cos(ang)}})
		rows = append(rows, meta{s.Time, o.LogValue, o.Censoring == bwq.Actual})
	}
	if len(obs) < 40 {
		return nil
	}
	fit := exceedance.FitRegression(obs, 3)
	if fit.Sigma == 0 {
		return nil
	}
	var out []resid
	for i, m := range rows {
		if !m.det {
			continue // residual only meaningful for an actual (uncensored) count
		}
		mu := fit.Mean(obs[i].Covars)
		yr, wk := m.t.ISOWeek()
		out = append(out, resid{region: region, yearWeek: yr*100 + wk, z: (m.lv - mu) / fit.Sigma})
	}
	return out
}

func report(residuals []resid) {
	// Standardise residuals to unit variance overall.
	var n float64
	var s2 float64
	for _, r := range residuals {
		s2 += r.z * r.z
		n++
	}
	sd := math.Sqrt(s2 / n)
	if sd == 0 {
		log.Fatal("degenerate residuals")
	}

	// Per region: sums for all-pairs and per-week sums for same-week pairs.
	type acc struct {
		s, q float64
		n    int
	}
	regionAll := map[string]*acc{}
	regionWeek := map[string]*acc{}
	for _, r := range residuals {
		z := r.z / sd
		ra := regionAll[r.region]
		if ra == nil {
			ra = &acc{}
			regionAll[r.region] = ra
		}
		ra.s += z
		ra.q += z * z
		ra.n++

		wk := fmt.Sprintf("%s|%d", r.region, r.yearWeek)
		rw := regionWeek[wk]
		if rw == nil {
			rw = &acc{}
			regionWeek[wk] = rw
		}
		rw.s += z
		rw.q += z * z
		rw.n++
	}

	pairProd := func(a *acc) (float64, float64) {
		return (a.s*a.s - a.q) / 2, float64(a.n*(a.n-1)) / 2
	}
	var sameProd, samePairs, allProd, allPairs float64
	for _, a := range regionWeek {
		p, c := pairProd(a)
		sameProd += p
		samePairs += c
	}
	for _, a := range regionAll {
		p, c := pairProd(a)
		allProd += p
		allPairs += c
	}
	diffProd := allProd - sameProd
	diffPairs := allPairs - samePairs

	rhoSame := safeDiv(sameProd, samePairs)
	rhoDiff := safeDiv(diffProd, diffPairs)

	fmt.Printf("\nRegional wet-week anomaly test — %.0f standardised residuals across %d regions\n", n, len(regionAll))
	fmt.Printf("  same-region, SAME-week residual correlation:      %+.3f  (%.0f pairs)\n", rhoSame, samePairs)
	fmt.Printf("  same-region, DIFFERENT-week residual correlation: %+.3f  (%.0f pairs)\n", rhoDiff, diffPairs)
	fmt.Printf("\n  Excess same-week correlation (the regional anomaly): %+.3f\n", rhoSame-rhoDiff)
	switch {
	case rhoSame-rhoDiff > 0.05:
		fmt.Printf("  → a coherent regional wet-week effect is present; a shared OU factor is justified.\n")
	case rhoSame-rhoDiff > 0.02:
		fmt.Printf("  → a weak regional effect; a shared factor may add explanatory value.\n")
	default:
		fmt.Printf("  → negligible; residuals are essentially site-idiosyncratic.\n")
	}
}

func safeDiv(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
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
	return out, nil
}
