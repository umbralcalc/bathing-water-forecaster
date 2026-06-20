// Command skill-ceiling decomposes how much exceedance skill is achievable. It
// fits a ladder of nested models of increasing richness and scores each one both
// in-sample (an optimistic ceiling — the best that covariate set could do with
// perfect estimation) and out-of-sample (what is realised once the parameters
// must be learned). The gaps answer two questions the PLAN poses:
//
//   - How much of exceedance is predictable at all, versus irreducible given
//     weekly, censored, rare-event sampling? (climatology vs the richest ceiling)
//
//   - Is deepening the model worth it? A covariate that lowers the in-sample
//     ceiling carries real signal; if the out-of-sample score also drops it is
//     worth building; if only the in-sample score drops, the limit is estimation
//     (pooling/calibration), not physics.
//
//     skill-ceiling -points 04700,04600,04800,03600,03900,04000,04250,10500
//     skill-ceiling -all -limit 120
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
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

// level is one rung of the model ladder: a label and which columns of the full
// covariate vector [rainNear, rainFar, seasonSin, seasonCos] it uses.
type level struct {
	name string
	cols []int
}

var ladder = []level{
	{"climatology", nil},
	{"+rain 2-day", []int{0}},
	{"+rain prior-week", []int{0, 1}},
	{"+season", []int{0, 1, 2, 3}},
}

// sample is one usable observation with the full covariate set precomputed and
// the outcome already resolved against the threshold.
type sample struct {
	covar       [4]float64
	logValue    float64
	censoring   bwq.Censoring
	exceeded    bool
	determinate bool
}

func main() {
	pointsCSV := flag.String("points", "04700,04600,04800,03600,03900,04000,04250,10500", "comma-separated points (ignored when -all)")
	all := flag.Bool("all", false, "decompose over every designated England site")
	limit := flag.Int("limit", 0, "cap sites when -all (0 = no cap)")
	dist := flag.Float64("dist", 15, "rain-gauge search radius (km)")
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
	log.Printf("skill-ceiling over %d site(s)", len(targets))

	// oracle = richest-possible fit ON the held-out period (the achievable ceiling
	// for that period); realised = fit on the earlier training period. Both are
	// scored on the identical held-out samples, so their gap is pure estimation
	// penalty, not a train/test base-rate shift.
	oracle := make([]scoreAcc, len(ladder))
	realised := make([]scoreAcc, len(ladder))
	var rich []forecast.Resolution // realised richest model, for the reliability curve
	nSites := 0

	for _, tgt := range targets {
		site, err := siteload.Load(ctx, bw, hy, tgt.Notation, tgt.Lat, tgt.Long, tgt.Name, *dist, 2)
		if err != nil {
			log.Printf("skip %s: %v", tgt.Notation, err)
			continue
		}
		train, test := splitSamples(site, *threshold, *testFrac)
		if len(train) < *minTrain || countDeterminate(test) < 8 {
			continue
		}
		nSites++

		for li, lv := range ladder {
			fitTrain := exceedance.FitRegression(project(train, lv.cols), len(lv.cols))
			fitTest := exceedance.FitRegression(project(test, lv.cols), len(lv.cols))
			scoreInto(&oracle[li], fitTest, test, lv.cols, *threshold)    // ceiling
			scoreInto(&realised[li], fitTrain, test, lv.cols, *threshold) // realised
			if li == len(ladder)-1 {
				rich = appendResolutions(rich, fitTrain, test, lv.cols, *threshold)
			}
		}
	}

	if nSites < 2 {
		log.Fatalf("need at least 2 usable sites, got %d", nSites)
	}
	report(nSites, oracle, realised, rich)
}

// splitSamples builds usable, time-ordered samples and holds out the latest
// testFrac. A sample is usable only if both rain windows (2-day and prior-week)
// have complete coverage, so every ladder rung scores the identical samples.
func splitSamples(site forecast.Site, threshold, testFrac float64) (train, test []sample) {
	type tagged struct {
		t time.Time
		s sample
	}
	var rows []tagged
	for _, s := range site.Samples {
		o, ok := exceedance.ObservationFromCount(s.EColi)
		if !ok {
			continue
		}
		near, cN := catchment.RainfallBetween(site.Rain, s.Time, 1, 0) // 2-day antecedent
		far, cF := catchment.RainfallBetween(site.Rain, s.Time, 6, 2)  // prior-week lag
		if cN != 2 || cF != 5 {
			continue
		}
		exc, det := forecast.Exceeded(s.EColi, threshold)
		ang := 2 * math.Pi * float64(s.Time.YearDay()) / 365.25
		rows = append(rows, tagged{s.Time, sample{
			covar:       [4]float64{near, far, math.Sin(ang), math.Cos(ang)},
			logValue:    o.LogValue,
			censoring:   o.Censoring,
			exceeded:    exc,
			determinate: det,
		}})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].t.Before(rows[j].t) })
	split := int(float64(len(rows)) * (1 - testFrac))
	for i, r := range rows {
		if i < split {
			train = append(train, r.s)
		} else {
			test = append(test, r.s)
		}
	}
	return train, test
}

// project turns samples into censored training observations carrying only the
// selected covariate columns (every sample, censored ones included).
func project(samples []sample, cols []int) []exceedance.CovObservation {
	out := make([]exceedance.CovObservation, len(samples))
	for i, s := range samples {
		cv := make([]float64, len(cols))
		for k, c := range cols {
			cv[k] = s.covar[c]
		}
		out[i] = exceedance.CovObservation{LogValue: s.logValue, Censoring: s.censoring, Covars: cv}
	}
	return out
}

// scoreInto evaluates a fit over the determinate samples and accumulates scores.
func scoreInto(acc *scoreAcc, fit exceedance.Regression, samples []sample, cols []int, threshold float64) {
	for _, s := range samples {
		if !s.determinate {
			continue
		}
		acc.add(fit.ExceedanceProb(pick(s, cols), threshold), s.exceeded)
	}
}

func appendResolutions(rs []forecast.Resolution, fit exceedance.Regression, samples []sample, cols []int, threshold float64) []forecast.Resolution {
	for _, s := range samples {
		if !s.determinate {
			continue
		}
		p := fit.ExceedanceProb(pick(s, cols), threshold)
		rs = append(rs, forecast.Resolution{
			Prediction: forecast.Prediction{PExceed: p},
			Exceeded:   s.exceeded,
			Brier:      forecast.Brier(p, s.exceeded),
			LogLoss:    forecast.LogLoss(p, s.exceeded),
		})
	}
	return rs
}

func pick(s sample, cols []int) []float64 {
	cv := make([]float64, len(cols))
	for k, c := range cols {
		cv[k] = s.covar[c]
	}
	return cv
}

func countDeterminate(samples []sample) int {
	n := 0
	for _, s := range samples {
		if s.determinate {
			n++
		}
	}
	return n
}

// scoreAcc accumulates log-loss and Brier.
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

func report(nSites int, oracle, realised []scoreAcc, rich []forecast.Resolution) {
	fmt.Printf("\nSkill-ceiling decomposition — %d sites, %d held-out determinate samples\n", nSites, realised[0].n)
	fmt.Printf("(oracle = covariate set fit ON the held-out period; realised = fit on earlier data; both scored on held-out)\n")

	fmt.Printf("\n  %-18s %12s %12s\n", "model", "oracle LL", "realised LL")
	for li, lv := range ladder {
		fmt.Printf("  %-18s %12.4f %12.4f\n", lv.name, oracle[li].logloss(), realised[li].logloss())
	}

	clim := oracle[0].logloss() // best constant on the held-out period = total uncertainty
	richestOracle := oracle[len(ladder)-1].logloss()
	richestReal := realised[len(ladder)-1].logloss()
	fmt.Printf("\n  Total uncertainty (climatology oracle LL):            %.4f\n", clim)
	fmt.Printf("  Achievable ceiling (richest oracle LL):               %.4f\n", richestOracle)
	fmt.Printf("  Realised (richest, trained on the past):              %.4f\n", richestReal)
	fmt.Printf("\n  Extractable signal (climatology − ceiling):           %.4f  (%+.0f%% of climatology)\n",
		clim-richestOracle, 100*(clim-richestOracle)/clim)
	fmt.Printf("  Realised gain   (climatology − realised):             %.4f  (%+.0f%% of climatology)\n",
		clim-richestReal, 100*(clim-richestReal)/clim)
	fmt.Printf("  Estimation gap  (realised − ceiling):                 %.4f  (recoverable by better estimation)\n",
		richestReal-richestOracle)

	fmt.Println("\n  Marginal value of each added covariate (Δ LL, negative = better):")
	for li := 1; li < len(ladder); li++ {
		fmt.Printf("    %-18s %+0.4f oracle   %+0.4f realised\n",
			ladder[li].name, oracle[li].logloss()-oracle[li-1].logloss(), realised[li].logloss()-realised[li-1].logloss())
	}

	fmt.Println("\n  Richest-model reliability (out-of-sample):")
	for _, b := range forecast.Aggregate(rich).ReliabilityBins {
		if b.N == 0 {
			continue
		}
		fmt.Printf("    %3.0f–%3.0f%%  mean %4.1f%% → %4.1f%% empirical (n=%d)\n",
			100*b.Lo, 100*b.Hi, 100*b.MeanP, 100*b.Empirical, b.N)
	}
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
