// Command close-gap decomposes the estimation gap between the realised model and
// the oracle ceiling into its recoverable parts, and measures how far two levers
// close it:
//
//   - recency: the realised baseline trains on a site's whole history, much of it
//     from a dirtier era; the bathing-water regime is non-stationary, so training
//     on only the recent past should help even though it uses less data.
//   - pooling: partially pooling the intercept and rain slope across sites by
//     empirical Bayes reduces the estimation noise the reduced recent window adds.
//
// Every variant is scored out-of-sample on the identical held-out samples, so the
// closed gap is read directly. Whatever remains after recency + pooling is the
// part of the gap that is genuinely irreducible from past data (the oracle peeks
// at the held-out period, so it is an optimistic, not fully attainable, ceiling).
//
//	close-gap -all -limit 160 -recent-years 6
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
	"github.com/umbralcalc/bathing-water-forecaster/internal/pooling"
	"github.com/umbralcalc/bathing-water-forecaster/internal/siteload"
)

// model bundles a coefficient vector and its covariate width for scoring.
type fitParams struct {
	b0, b1, sigma float64
	v0, v1        float64
}

type siteFit struct {
	point, region string
	full          fitParams // trained on the whole history
	recent        fitParams // trained on the recent window only
	climB0, climS float64   // intercept-only on full history
	orB0, orB1    float64   // in-sample oracle: fit on the whole held-out period
	orS           float64
	// Cross-validated (fair) oracle: each test half is predicted by a model fit
	// on the OTHER test half — test-era training, but no peeking at the scored
	// samples. fairEarly scores the early half; fairLate scores the late half.
	fairEarly fitParams
	fairLate  fitParams
	test      []testRow
}

type testRow struct {
	rain     float64
	exceeded bool
	late     bool // in the second half of the held-out period
}

func main() {
	pointsCSV := flag.String("points", "04700,04600,04800,04500,03600,03900,04000,04250,03700,10500", "comma-separated points (ignored when -all)")
	all := flag.Bool("all", false, "run over every designated England site")
	limit := flag.Int("limit", 0, "cap sites when -all (0 = no cap)")
	dist := flag.Float64("dist", 15, "rain-gauge search radius (km)")
	window := flag.Int("window", 2, "antecedent rainfall window (days)")
	threshold := flag.Float64("threshold", 500, "E. coli exceedance cut (count/100ml)")
	minTrain := flag.Int("min-train", 40, "minimum full-history training samples")
	minRecent := flag.Int("min-recent", 25, "minimum recent-window training samples")
	recentYears := flag.Float64("recent-years", 6, "length of the recent training window (years)")
	testFrac := flag.Float64("test-frac", 0.3, "fraction of each site's latest samples held out")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	bw := bwq.New()
	hy := hydro.New()

	targets, err := resolveTargets(ctx, bw, *all, *limit, *pointsCSV)
	if err != nil {
		log.Fatalf("resolve targets: %v", err)
	}
	log.Printf("close-gap over %d site(s)", len(targets))

	var sites []siteFit
	for _, tgt := range targets {
		site, err := siteload.Load(ctx, bw, hy, tgt.Notation, tgt.Lat, tgt.Long, tgt.Name, *dist, *window)
		if err != nil {
			log.Printf("skip %s: %v", tgt.Notation, err)
			continue
		}
		if f, ok := fitOne(site, *threshold, *window, *minTrain, *minRecent, *recentYears, *testFrac); ok {
			sites = append(sites, f)
		}
	}
	if len(sites) < 3 {
		log.Fatalf("need at least 3 fitted sites, got %d", len(sites))
	}

	// Pool intercept and slope across sites, separately for the full-history and
	// recent-window fits.
	fullB0 := poolField(sites, func(f siteFit) (float64, float64, string) { return f.full.b0, f.full.v0, f.region })
	fullB1 := poolField(sites, func(f siteFit) (float64, float64, string) { return f.full.b1, f.full.v1, f.region })
	recB0 := poolField(sites, func(f siteFit) (float64, float64, string) { return f.recent.b0, f.recent.v0, f.region })
	recB1 := poolField(sites, func(f siteFit) (float64, float64, string) { return f.recent.b1, f.recent.v1, f.region })

	labels := []string{
		"climatology (full)",
		"realised: unpooled full",
		"+ pooling (full)",
		"+ recency",
		"+ recency + pooling",
		"fair oracle (CV on test)",
		"in-sample oracle",
	}
	acc := make([]scoreAcc, len(labels))
	for _, f := range sites {
		fixed := [][]float64{
			{f.climB0},
			{f.full.b0, f.full.b1},
			{fullB0[f.point], fullB1[f.point]},
			{f.recent.b0, f.recent.b1},
			{recB0[f.point], recB1[f.point]},
		}
		sigmas := []float64{f.climS, f.full.sigma, f.full.sigma, f.recent.sigma, f.recent.sigma}
		for _, tr := range f.test {
			for vi, beta := range fixed {
				var cov []float64
				if len(beta) == 2 {
					cov = []float64{tr.rain}
				}
				m := exceedance.Regression{Beta: beta, Sigma: sigmas[vi]}
				acc[vi].add(m.ExceedanceProb(cov, *threshold), tr.exceeded)
			}
			// Fair oracle: predict each test half with the model fit on the other.
			fp := f.fairEarly
			if tr.late {
				fp = f.fairLate
			}
			fair := exceedance.Regression{Beta: []float64{fp.b0, fp.b1}, Sigma: fp.sigma}
			acc[5].add(fair.ExceedanceProb([]float64{tr.rain}, *threshold), tr.exceeded)
			// In-sample oracle.
			ins := exceedance.Regression{Beta: []float64{f.orB0, f.orB1}, Sigma: f.orS}
			acc[6].add(ins.ExceedanceProb([]float64{tr.rain}, *threshold), tr.exceeded)
		}
	}

	report(len(sites), *recentYears, labels, acc)
}

func fitOne(site forecast.Site, threshold float64, window, minTrain, minRecent int, recentYears, testFrac float64) (siteFit, bool) {
	type tagged struct {
		t        time.Time
		obs      exceedance.CovObservation
		rain     float64
		rainOK   bool
		exceeded bool
		det      bool
	}
	var rows []tagged
	for _, s := range site.Samples {
		o, ok := exceedance.ObservationFromCount(s.EColi)
		if !ok {
			continue
		}
		rmm, cov := catchment.AntecedentRainfall(site.Rain, s.Time, window)
		exc, det := forecast.Exceeded(s.EColi, threshold)
		rows = append(rows, tagged{
			t:    s.Time,
			obs:  exceedance.CovObservation{LogValue: o.LogValue, Censoring: o.Censoring, Covars: []float64{rmm}},
			rain: rmm, rainOK: cov == window, exceeded: exc, det: det,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].t.Before(rows[j].t) })
	split := int(float64(len(rows)) * (1 - testFrac))
	if split <= 0 || split >= len(rows) {
		return siteFit{}, false
	}
	recentCutoff := rows[split].t.AddDate(-int(recentYears), 0, 0)

	var fullObs, recentObs, testObs []exceedance.CovObservation
	type ti struct {
		obs      exceedance.CovObservation
		rain     float64
		exceeded bool
		det      bool
	}
	var testItems []ti
	for i, r := range rows {
		if !r.rainOK {
			continue
		}
		if i < split {
			fullObs = append(fullObs, r.obs)
			if !r.t.Before(recentCutoff) {
				recentObs = append(recentObs, r.obs)
			}
		} else {
			testObs = append(testObs, r.obs)
			testItems = append(testItems, ti{obs: r.obs, rain: r.rain, exceeded: r.exceeded, det: r.det})
		}
	}
	// The cross-validated oracle needs both test halves big enough to fit.
	half := len(testItems) / 2
	if len(fullObs) < minTrain || len(recentObs) < minRecent || len(testObs) < 30 || half < 12 {
		return siteFit{}, false
	}

	earlyObs := make([]exceedance.CovObservation, 0, half)
	lateObs := make([]exceedance.CovObservation, 0, len(testItems)-half)
	var test []testRow
	for i, it := range testItems {
		if i < half {
			earlyObs = append(earlyObs, it.obs)
		} else {
			lateObs = append(lateObs, it.obs)
		}
		if it.det {
			test = append(test, testRow{rain: it.rain, exceeded: it.exceeded, late: i >= half})
		}
	}
	if len(test) < 8 {
		return siteFit{}, false
	}

	region := ""
	if len(site.Samples) > 0 {
		region = bwq.RegionOf(site.Samples[0].BathingWaterID)
	}
	full := fitParamsFrom(fullObs)
	recent := fitParamsFrom(recentObs)
	clim := exceedance.FitRegression(stripCovars(fullObs), 0)
	oracle := exceedance.FitRegression(testObs, 1)

	return siteFit{
		point: site.Point, region: region,
		full: full, recent: recent,
		climB0: clim.Beta[0], climS: clim.Sigma,
		orB0: oracle.Beta[0], orB1: oracle.Beta[1], orS: oracle.Sigma,
		fairEarly: fitParamsFrom(lateObs),  // predicts the early half
		fairLate:  fitParamsFrom(earlyObs), // predicts the late half
		test:      test,
	}, true
}

func fitParamsFrom(obs []exceedance.CovObservation) fitParams {
	fit := exceedance.FitRegression(obs, 1)
	v := fit.CoefVariances(obs)
	return fitParams{b0: fit.Beta[0], b1: fit.Beta[1], sigma: fit.Sigma, v0: v[0], v1: v[1]}
}

func stripCovars(obs []exceedance.CovObservation) []exceedance.CovObservation {
	out := make([]exceedance.CovObservation, len(obs))
	for i, o := range obs {
		out[i] = exceedance.CovObservation{LogValue: o.LogValue, Censoring: o.Censoring}
	}
	return out
}

// poolField pools one coefficient (selected by get) across sites and returns the
// pooled value keyed by site point. Regional pooling is used (PoolByGroup), which
// matched or beat national in the slope experiment.
func poolField(sites []siteFit, get func(siteFit) (val, varc float64, group string)) map[string]float64 {
	ests := make([]pooling.Estimate, len(sites))
	for i, f := range sites {
		v, vc, g := get(f)
		ests[i] = pooling.Estimate{Key: f.point, Group: g, Value: v, Variance: vc}
	}
	out := make(map[string]float64, len(sites))
	for _, p := range pooling.PoolByGroup(ests) {
		out[p.Key] = p.Pooled
	}
	return out
}

type scoreAcc struct {
	n      int
	sumLog float64
}

func (s *scoreAcc) add(p float64, y bool) { s.n++; s.sumLog += forecast.LogLoss(p, y) }
func (s scoreAcc) logloss() float64 {
	if s.n == 0 {
		return 0
	}
	return s.sumLog / float64(s.n)
}

func report(nSites int, recentYears float64, labels []string, acc []scoreAcc) {
	fmt.Printf("\nClose-gap experiment — %d sites, %d held-out samples, recent window %.0fy\n", nSites, acc[0].n, recentYears)
	fmt.Printf("\n  %-26s %10s\n", "model", "logloss")
	for i, l := range labels {
		fmt.Printf("  %-26s %10.4f\n", l, acc[i].logloss())
	}

	realised := acc[1].logloss()
	fair := acc[5].logloss()     // attainable ceiling
	insample := acc[6].logloss() // optimistic ceiling (peeks at the scored samples)
	best := acc[4].logloss()     // recency + pooling

	insampleGap := realised - insample
	fairGap := realised - fair
	fmt.Printf("\n  Apparent gap (realised − in-sample oracle):  %.4f\n", insampleGap)
	fmt.Printf("  Oracle optimism (fair − in-sample oracle):   %.4f  (%.0f%% of the apparent gap is unattainable)\n",
		fair-insample, 100*(fair-insample)/insampleGap)
	fmt.Printf("  Attainable gap (realised − fair oracle):     %.4f\n", fairGap)

	if fairGap <= 1e-9 {
		fmt.Printf("\n  Best model (recency+pooling) already meets the fair ceiling — gap closed.\n")
		return
	}
	fmt.Println("\n  Share of the ATTAINABLE gap closed by each lever (vs realised):")
	for i := 2; i <= 4; i++ {
		fmt.Printf("    %-24s %+6.0f%%  (logloss %.4f)\n", labels[i], 100*(realised-acc[i].logloss())/fairGap, acc[i].logloss())
	}
	fmt.Printf("\n  Remaining attainable gap after best model: %.4f  (%.0f%% of attainable still open)\n",
		best-fair, 100*(best-fair)/fairGap)
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
