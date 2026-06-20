// Command pool-slope tests the thesis from skill-ceiling: that the forecaster is
// estimation-limited, not covariate-limited, so partially pooling the rain
// coefficient across sites should recover part of the gap between the realised
// model (fit per site on the past) and the oracle ceiling (fit on the held-out
// period). It fits the one-covariate model (2-day antecedent rain) per site,
// pools the slope toward a regional and a national mean by empirical Bayes, and
// scores every variant out-of-sample on the identical held-out samples.
//
//	pool-slope -all -limit 130
//	pool-slope -points 04700,04600,04800,03600,03900,04000,04250,10500
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

type siteFit struct {
	point, region       string
	b0, b1, sigma       float64
	v0, v1              float64
	climB0, climSigma   float64 // intercept-only (climatology)
	orB0, orB1, orSigma float64 // oracle: fit on the held-out period
	test                []testRow
}

type testRow struct {
	rain     float64
	exceeded bool
}

func main() {
	pointsCSV := flag.String("points", "04700,04600,04800,04500,03600,03900,04000,04250,03700,10500", "comma-separated points (ignored when -all)")
	all := flag.Bool("all", false, "run over every designated England site")
	limit := flag.Int("limit", 0, "cap sites when -all (0 = no cap)")
	dist := flag.Float64("dist", 15, "rain-gauge search radius (km)")
	window := flag.Int("window", 2, "antecedent rainfall window (days)")
	threshold := flag.Float64("threshold", 500, "E. coli exceedance cut (count/100ml)")
	minTrain := flag.Int("min-train", 40, "minimum training samples to include a site")
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
	log.Printf("pool-slope over %d site(s)", len(targets))

	var sites []siteFit
	for _, tgt := range targets {
		site, err := siteload.Load(ctx, bw, hy, tgt.Notation, tgt.Lat, tgt.Long, tgt.Name, *dist, *window)
		if err != nil {
			log.Printf("skip %s: %v", tgt.Notation, err)
			continue
		}
		f, ok := fitOne(site, *threshold, *window, *minTrain, *testFrac)
		if ok {
			sites = append(sites, f)
		}
	}
	if len(sites) < 3 {
		log.Fatalf("need at least 3 fitted sites, got %d", len(sites))
	}

	// Pool the slope (β1) and the intercept (β0) across sites.
	slopeEst := make([]pooling.Estimate, len(sites))
	interEst := make([]pooling.Estimate, len(sites))
	for i, f := range sites {
		slopeEst[i] = pooling.Estimate{Key: f.point, Group: f.region, Value: f.b1, Variance: f.v1, N: 0}
		interEst[i] = pooling.Estimate{Key: f.point, Group: f.region, Value: f.b0, Variance: f.v0, N: 0}
	}
	_, slopeNatP := pooling.Pool(slopeEst)
	slopeNat := byKey(slopeNatP)
	slopeReg := byKey(pooling.PoolByGroup(slopeEst))
	_, interNatP := pooling.Pool(interEst)
	interNat := byKey(interNatP)

	// Score every variant out-of-sample on the identical held-out samples.
	labels := []string{"climatology", "unpooled (per-site)", "pooled slope (regional)", "pooled slope (national)", "pooled slope+intercept", "oracle ceiling"}
	acc := make([]scoreAcc, len(labels))
	for _, f := range sites {
		models := []exceedance.Regression{
			{Beta: []float64{f.climB0}, Sigma: f.climSigma},
			{Beta: []float64{f.b0, f.b1}, Sigma: f.sigma},
			{Beta: []float64{f.b0, slopeReg[f.point]}, Sigma: f.sigma},
			{Beta: []float64{f.b0, slopeNat[f.point]}, Sigma: f.sigma},
			{Beta: []float64{interNat[f.point], slopeNat[f.point]}, Sigma: f.sigma},
			{Beta: []float64{f.orB0, f.orB1}, Sigma: f.orSigma},
		}
		for mi, m := range models {
			covarLen := len(m.Beta) - 1
			for _, tr := range f.test {
				var cov []float64
				if covarLen == 1 {
					cov = []float64{tr.rain}
				}
				acc[mi].add(m.ExceedanceProb(cov, *threshold), tr.exceeded)
			}
		}
	}

	report(len(sites), labels, acc)
}

func fitOne(site forecast.Site, threshold float64, window, minTrain int, testFrac float64) (siteFit, bool) {
	type tagged struct {
		t        time.Time
		lv       float64
		cens     bwq.Censoring
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
		rows = append(rows, tagged{s.Time, o.LogValue, o.Censoring, rmm, cov == window, exc, det})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].t.Before(rows[j].t) })
	split := int(float64(len(rows)) * (1 - testFrac))

	var trainObs, testObs []exceedance.CovObservation
	var test []testRow
	for i, r := range rows {
		if !r.rainOK {
			continue
		}
		o := exceedance.CovObservation{LogValue: r.lv, Censoring: r.cens, Covars: []float64{r.rain}}
		if i < split {
			trainObs = append(trainObs, o)
		} else {
			testObs = append(testObs, o)
			if r.det {
				test = append(test, testRow{rain: r.rain, exceeded: r.exceeded})
			}
		}
	}
	if len(trainObs) < minTrain || len(test) < 8 || len(testObs) < 15 {
		return siteFit{}, false
	}

	fit := exceedance.FitRegression(trainObs, 1)
	vars := fit.CoefVariances(trainObs)
	// Climatology is the intercept-only fit; it must see covariate-free observations.
	climObs := make([]exceedance.CovObservation, len(trainObs))
	for i, o := range trainObs {
		climObs[i] = exceedance.CovObservation{LogValue: o.LogValue, Censoring: o.Censoring}
	}
	clim := exceedance.FitRegression(climObs, 0)
	oracle := exceedance.FitRegression(testObs, 1)

	region := ""
	if len(site.Samples) > 0 {
		region = bwq.RegionOf(site.Samples[0].BathingWaterID)
	}
	return siteFit{
		point: site.Point, region: region,
		b0: fit.Beta[0], b1: fit.Beta[1], sigma: fit.Sigma,
		v0: vars[0], v1: vars[1],
		climB0: clim.Beta[0], climSigma: clim.Sigma,
		orB0: oracle.Beta[0], orB1: oracle.Beta[1], orSigma: oracle.Sigma,
		test: test,
	}, true
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

func report(nSites int, labels []string, acc []scoreAcc) {
	fmt.Printf("\nSlope-pooling experiment — %d sites, %d held-out determinate samples\n", nSites, acc[0].n)
	fmt.Printf("\n  %-26s %10s\n", "model", "logloss")
	for i, l := range labels {
		fmt.Printf("  %-26s %10.4f\n", l, acc[i].logloss())
	}

	clim := acc[0].logloss()
	unpooled := acc[1].logloss()
	slopeReg := acc[2].logloss()
	slopeNat := acc[3].logloss()
	both := acc[4].logloss()
	oracle := acc[5].logloss()

	gap := unpooled - oracle
	fmt.Printf("\n  Estimation gap to close (unpooled − oracle): %.4f\n", gap)
	if gap > 1e-9 {
		fmt.Printf("  Gap recovered by pooling the slope (regional):  %+.0f%%\n", 100*(unpooled-slopeReg)/gap)
		fmt.Printf("  Gap recovered by pooling the slope (national):  %+.0f%%\n", 100*(unpooled-slopeNat)/gap)
		fmt.Printf("  Gap recovered by pooling slope + intercept:     %+.0f%%\n", 100*(unpooled-both)/gap)
	} else {
		fmt.Printf("  (unpooled already meets/beats the oracle — no gap to recover)\n")
	}
	fmt.Printf("\n  vs climatology (%.4f): unpooled %+.4f, national-slope %+.4f, slope+intercept %+.4f\n",
		clim, unpooled-clim, slopeNat-clim, both-clim)
}

func byKey(pooled []pooling.Pooled) map[string]float64 {
	m := make(map[string]float64, len(pooled))
	for _, p := range pooled {
		m[p.Key] = p.Pooled
	}
	return m
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
