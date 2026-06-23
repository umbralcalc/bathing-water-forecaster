// Command export-dashboard fits the rain+season model for a set of sites and
// writes their coefficients, locations, and recent resolved samples to
// dashboard/data.js as `window.FORECAST_DATA`. The static dashboard then computes
// P(exceedance) client-side from these coefficients — the idea of shipping
// the fitted model to the reader so the rainfall→exceedance explorer runs live.
//
//	export-dashboard -all -limit 140 -out dashboard/data.js
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/umbralcalc/bathing-water-forecaster/internal/bwq"
	"github.com/umbralcalc/bathing-water-forecaster/internal/catchment"
	"github.com/umbralcalc/bathing-water-forecaster/internal/exceedance"
	"github.com/umbralcalc/bathing-water-forecaster/internal/forecast"
	"github.com/umbralcalc/bathing-water-forecaster/internal/hydro"
	"github.com/umbralcalc/bathing-water-forecaster/internal/siteload"
)

type sampleOut struct {
	Date  string  `json:"date"`
	Rain  float64 `json:"rain"`
	State int     `json:"state"` // 1 exceeded, 0 ok, -1 censored-below (definitely ok)
}

type siteOut struct {
	Point  string      `json:"point"`
	Name   string      `json:"name"`
	Region string      `json:"region"`
	Lat    float64     `json:"lat"`
	Long   float64     `json:"long"`
	Gauge  string      `json:"gauge"`
	Beta   [4]float64  `json:"beta"` // [intercept, rain, sin, cos]
	Sigma  float64     `json:"sigma"`
	N      int         `json:"n"`
	Sample []sampleOut `json:"samples"`
}

type dataOut struct {
	Generated string    `json:"generated"`
	Threshold float64   `json:"threshold"`
	Window    int       `json:"window"`
	Sites     []siteOut `json:"sites"`
}

func main() {
	pointsCSV := flag.String("points", "", "comma-separated points (default: -all)")
	all := flag.Bool("all", true, "use every designated England site")
	limit := flag.Int("limit", 140, "cap sites when -all")
	dist := flag.Float64("dist", 15, "rain-gauge search radius (km)")
	window := flag.Int("window", 2, "antecedent rainfall window (days)")
	threshold := flag.Float64("threshold", 500, "E. coli exceedance cut")
	recent := flag.Int("recent", 24, "recent resolved samples to include per site")
	workers := flag.Int("workers", 8, "concurrent site fetches (higher risks EA API throttling)")
	cacheDir := flag.String("cache", "data/raw/sites", "directory for cached site pulls (\"\" disables)")
	maxAge := flag.Duration("max-age", 7*24*time.Hour, "refetch a cached site once it is older than this (0 = never)")
	refresh := flag.Bool("refresh", false, "ignore the cache and refetch every site")
	out := flag.String("out", "dashboard/data.js", "output file")
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
	log.Printf("exporting %d site(s) with %d workers", len(targets), *workers)

	data := dataOut{
		Generated: time.Now().UTC().Format("2006-01-02"),
		Threshold: *threshold,
		Window:    *window,
	}

	// The per-site work is independent and network-bound, so fan it out across a
	// worker pool; results are collected on the main goroutine (no shared state).
	jobs := make(chan bwq.SamplingPoint)
	results := make(chan siteOut)
	var hits, fresh int64
	var wg sync.WaitGroup
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for tgt := range jobs {
				site, cached, err := siteload.LoadCached(ctx, bw, hy, tgt.Notation, tgt.Lat, tgt.Long, tgt.Name, *dist, *window, *cacheDir, *maxAge, *refresh)
				if err != nil {
					continue
				}
				if cached {
					atomic.AddInt64(&hits, 1)
				} else {
					atomic.AddInt64(&fresh, 1)
				}
				if so, ok := exportSite(site, *window, *threshold, *recent); ok {
					results <- so
				}
			}
		}()
	}
	go func() {
		for _, tgt := range targets {
			jobs <- tgt
		}
		close(jobs)
	}()
	go func() { wg.Wait(); close(results) }()

	done := 0
	for so := range results {
		data.Sites = append(data.Sites, so)
		if done++; done%50 == 0 {
			log.Printf("  %d sites fetched", done)
		}
	}
	if len(data.Sites) == 0 {
		log.Fatal("no sites exported")
	}
	sort.Slice(data.Sites, func(i, j int) bool { return data.Sites[i].Name < data.Sites[j].Name })

	blob, err := json.MarshalIndent(data, "", " ")
	if err != nil {
		log.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(*out, []byte("window.FORECAST_DATA = "+string(blob)+";\n"), 0o644); err != nil {
		log.Fatalf("write: %v", err)
	}
	fmt.Printf("wrote %d sites to %s (%d from cache, %d freshly fetched)\n",
		len(data.Sites), *out, hits, fresh)
}

// plausibleFit rejects degenerate fits — typically sites where nearly every
// count is censored ("< limit"), so the latent mean is unidentifiable and the
// optimiser collapses σ→0 with exploding coefficients. Such a fit produces a
// meaningless step-function forecast, so it is excluded from the dashboard rather
// than shown. Bounds are deliberately generous; real bacterial log-counts have
// σ ≈ 1–3, per-mm rain effects well under 1, and seasonal swings under ~e⁴×.
func plausibleFit(f exceedance.Regression) bool {
	seasonAmp := math.Hypot(f.Beta[2], f.Beta[3])
	return f.Sigma >= 0.3 && f.Sigma <= 10 &&
		math.Abs(f.Beta[0]) <= 20 &&
		math.Abs(f.Beta[1]) <= 1.0 &&
		seasonAmp <= 4
}

func exportSite(site forecast.Site, window int, threshold float64, recent int) (siteOut, bool) {
	type meta struct {
		t     time.Time
		rain  float64
		state int
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
		exc, det := forecast.Exceeded(s.EColi, threshold)
		st := 0
		switch {
		case !det:
			st = -1
		case exc:
			st = 1
		}
		rows = append(rows, meta{s.Time, rmm, st})
	}
	if len(obs) < 40 {
		return siteOut{}, false
	}
	fit := exceedance.FitRegression(obs, 3)
	if !plausibleFit(fit) {
		return siteOut{}, false // degenerate fit (e.g. all-censored site, σ→0)
	}

	// Most-recent resolved samples for the overlay.
	sort.Slice(rows, func(i, j int) bool { return rows[i].t.After(rows[j].t) })
	if len(rows) > recent {
		rows = rows[:recent]
	}
	samples := make([]sampleOut, 0, len(rows))
	for _, m := range rows {
		samples = append(samples, sampleOut{Date: m.t.Format("2006-01-02"), Rain: round1(m.rain), State: m.state})
	}

	region := ""
	if len(site.Samples) > 0 {
		region = bwq.RegionOf(site.Samples[0].BathingWaterID)
	}
	return siteOut{
		Point: site.Point, Name: site.Name, Region: region,
		Lat: round4(site.Lat), Long: round4(site.Long), Gauge: site.Gauge,
		Beta:  [4]float64{round4(fit.Beta[0]), round5(fit.Beta[1]), round4(fit.Beta[2]), round4(fit.Beta[3])},
		Sigma: round4(fit.Sigma), N: len(obs), Sample: samples,
	}, true
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }
func round4(x float64) float64 { return math.Round(x*1e4) / 1e4 }
func round5(x float64) float64 { return math.Round(x*1e5) / 1e5 }

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
