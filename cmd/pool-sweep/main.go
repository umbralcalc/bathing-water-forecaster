// Command pool-sweep selects the pooling depth for the site baseline empirically.
// It fits each site's dry-day baseline (the regression intercept) on a training
// span, partially pools those baselines at two depths — within NUTS1 region, and
// nationally — and scores all three options (no pooling / regional / national) on
// a held-out span by out-of-sample exceedance log-loss, the chosen
// criterion. The headline question: does borrowing strength for the baseline help,
// and most of all at the sparse, data-poor sites?
//
//	pool-sweep -all -limit 120 -test-frac 0.3
//	pool-sweep -points 04700,04600,03600,03900,04000
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

// fitted holds one site's trained model plus its held-out test rows.
type fitted struct {
	point     string
	name      string
	region    string
	beta1     float64
	sigma     float64
	intercept float64
	interVar  float64
	trainN    int
	test      []testRow
}

type testRow struct {
	rain     float64
	exceeded bool
}

func main() {
	pointsCSV := flag.String("points", "04700,04600,04800,03600,03900,04000,04250,10500", "comma-separated points (ignored when -all)")
	all := flag.Bool("all", false, "sweep over every designated England site")
	limit := flag.Int("limit", 0, "cap sites when -all (0 = no cap)")
	dist := flag.Float64("dist", 15, "rain-gauge search radius (km)")
	window := flag.Int("window", 2, "antecedent rainfall window (days)")
	threshold := flag.Float64("threshold", 500, "E. coli exceedance cut (count/100ml)")
	minTrain := flag.Int("min-train", 30, "minimum training samples to include a site")
	testFrac := flag.Float64("test-frac", 0.3, "fraction of each site's latest samples held out")
	sparseN := flag.Int("sparse-n", 80, "sites with fewer than this many train samples count as sparse")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	bw := bwq.New()
	hy := hydro.New()

	targets, err := resolveTargets(ctx, bw, *all, *limit, *pointsCSV)
	if err != nil {
		log.Fatalf("resolve targets: %v", err)
	}
	log.Printf("pool-sweep over %d site(s)", len(targets))

	var sites []fitted
	for _, tgt := range targets {
		site, err := siteload.Load(ctx, bw, hy, tgt.Notation, tgt.Lat, tgt.Long, tgt.Name, *dist, *window)
		if err != nil {
			log.Printf("skip %s: %v", tgt.Notation, err)
			continue
		}
		f, ok := fitSite(site, *threshold, *minTrain, *testFrac)
		if !ok {
			continue
		}
		sites = append(sites, f)
	}
	if len(sites) < 2 {
		log.Fatalf("need at least 2 fitted sites, got %d", len(sites))
	}

	// Build pooling inputs from the trained intercepts.
	ests := make([]pooling.Estimate, len(sites))
	for i, f := range sites {
		ests[i] = pooling.Estimate{Key: f.point, Group: f.region, Value: f.intercept, Variance: f.interVar, N: f.trainN}
	}
	_, nationalPooled := pooling.Pool(ests)
	national := byKey(nationalPooled)
	regional := byKey(pooling.PoolByGroup(ests))

	// Score each depth out-of-sample, overall and on the sparse subset.
	var overall, sparse [3]scoreAcc
	for _, f := range sites {
		intercepts := [3]float64{
			f.intercept,       // none
			regional[f.point], // regional
			national[f.point], // national
		}
		isSparse := f.trainN < *sparseN
		for d := 0; d < 3; d++ {
			model := exceedance.Regression{Beta: []float64{intercepts[d], f.beta1}, Sigma: f.sigma}
			for _, tr := range f.test {
				p := model.ExceedanceProb([]float64{tr.rain}, *threshold)
				overall[d].add(p, tr.exceeded)
				if isSparse {
					sparse[d].add(p, tr.exceeded)
				}
			}
		}
	}

	reportSweep(sites, *sparseN, overall, sparse)
}

// fitSite trains a site's model on the earliest (1-testFrac) of its samples and
// returns it with the held-out test rows.
func fitSite(site forecast.Site, threshold float64, minTrain int, testFrac float64) (fitted, bool) {
	type r struct {
		t        time.Time
		lv       float64
		cens     bwq.Censoring
		rain     float64
		rainOK   bool
		exceeded bool
		det      bool
	}
	var rows []r
	for _, s := range site.Samples {
		o, ok := exceedance.ObservationFromCount(s.EColi)
		if !ok {
			continue
		}
		rmm, cov := catchment.AntecedentRainfall(site.Rain, s.Time, site.WindowDays)
		exc, det := forecast.Exceeded(s.EColi, threshold)
		rows = append(rows, r{s.Time, o.LogValue, o.Censoring, rmm, cov == site.WindowDays, exc, det})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].t.Before(rows[j].t) })

	split := int(float64(len(rows)) * (1 - testFrac))
	var train []exceedance.CovObservation
	for _, row := range rows[:split] {
		if row.rainOK {
			train = append(train, exceedance.CovObservation{LogValue: row.lv, Censoring: row.cens, Covars: []float64{row.rain}})
		}
	}
	if len(train) < minTrain {
		return fitted{}, false
	}
	var test []testRow
	for _, row := range rows[split:] {
		if row.rainOK && row.det {
			test = append(test, testRow{rain: row.rain, exceeded: row.exceeded})
		}
	}
	if len(test) < 5 {
		return fitted{}, false
	}

	fit := exceedance.FitRegression(train, 1)
	region := ""
	if len(site.Samples) > 0 {
		region = bwq.RegionOf(site.Samples[0].BathingWaterID)
	}
	return fitted{
		point: site.Point, name: site.Name, region: region,
		beta1: fit.Beta[1], sigma: fit.Sigma,
		intercept: fit.Beta[0], interVar: fit.InterceptVariance(train),
		trainN: len(train), test: test,
	}, true
}

// scoreAcc accumulates log-loss and Brier over scored samples.
type scoreAcc struct {
	n             int
	sumLog, sumBr float64
}

func (s *scoreAcc) add(p float64, y bool) {
	s.n++
	s.sumLog += forecast.LogLoss(p, y)
	s.sumBr += forecast.Brier(p, y)
}
func (s scoreAcc) logloss() float64 {
	if s.n == 0 {
		return 0
	}
	return s.sumLog / float64(s.n)
}
func (s scoreAcc) brier() float64 {
	if s.n == 0 {
		return 0
	}
	return s.sumBr / float64(s.n)
}

func reportSweep(sites []fitted, sparseN int, overall, sparse [3]scoreAcc) {
	nSparse := 0
	regions := map[string]bool{}
	for _, f := range sites {
		if f.trainN < sparseN {
			nSparse++
		}
		regions[f.region] = true
	}
	fmt.Printf("\nPool-sweep — %d sites across %d regions; %d held-out samples (%d from sparse sites <%d train)\n",
		len(sites), len(regions), overall[0].n, sparse[0].n, sparseN)

	labels := [3]string{"none (per-site)", "regional", "national"}
	fmt.Printf("\n  %-18s %10s %10s\n", "pooling depth", "logloss", "Brier")
	for d := 0; d < 3; d++ {
		fmt.Printf("  %-18s %10.4f %10.4f\n", labels[d], overall[d].logloss(), overall[d].brier())
	}
	best := 0
	for d := 1; d < 3; d++ {
		if overall[d].logloss() < overall[best].logloss() {
			best = d
		}
	}
	fmt.Printf("  → best out-of-sample log-loss: %s\n", labels[best])

	fmt.Printf("\n  Sparse sites only (<%d train samples, n=%d held-out):\n", sparseN, sparse[0].n)
	fmt.Printf("  %-18s %10s %10s\n", "pooling depth", "logloss", "Brier")
	for d := 0; d < 3; d++ {
		fmt.Printf("  %-18s %10.4f %10.4f\n", labels[d], sparse[d].logloss(), sparse[d].brier())
	}
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
