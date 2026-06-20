// Command anomaly-smc measures whether inferring the shared regional anomaly
// actually improves forecasts. For every region-week in which several sites are
// sampled, it holds one site out, infers the week's anomaly z from the OTHER
// sites by the particle filter (PosteriorZ, handling their censoring), and uses
// that z to condition the held-out site's exceedance forecast — then scores it
// against the no-anomaly forecast on the held-out site's actual outcome.
//
// This is the operational payoff of the latent factor: knowing how the rest of
// the coast is doing this week to sharpen a site not yet sampled. Given the weak
// measured effect (~0.05 excess residual correlation) the gain is expected to be
// small; the point is to measure it honestly.
//
//	anomaly-smc -all -limit 220 -rho 0.05
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/umbralcalc/bathing-water-forecaster/internal/anomaly"
	"github.com/umbralcalc/bathing-water-forecaster/internal/bwq"
	"github.com/umbralcalc/bathing-water-forecaster/internal/catchment"
	"github.com/umbralcalc/bathing-water-forecaster/internal/exceedance"
	"github.com/umbralcalc/bathing-water-forecaster/internal/forecast"
	"github.com/umbralcalc/bathing-water-forecaster/internal/hydro"
	"github.com/umbralcalc/bathing-water-forecaster/internal/siteload"
)

type member struct {
	key       string // region|year|week
	mu, sigma float64
	logValue  float64
	censoring bwq.Censoring
	exceeded  bool
	det       bool
}

func main() {
	pointsCSV := flag.String("points", "", "comma-separated points (default: -all)")
	all := flag.Bool("all", true, "use every designated England site")
	limit := flag.Int("limit", 220, "cap sites when -all")
	dist := flag.Float64("dist", 15, "rain-gauge search radius (km)")
	window := flag.Int("window", 2, "antecedent rainfall window (days)")
	threshold := flag.Float64("threshold", 500, "E. coli exceedance cut")
	rho := flag.Float64("rho", 0.05, "shared variance fraction (within-week residual correlation)")
	flag.Parse()
	if *pointsCSV != "" {
		*all = false
	}
	logThr := math.Log(*threshold)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()
	bw, hy := bwq.New(), hydro.New()

	targets, err := resolveTargets(ctx, bw, *all, *limit, *pointsCSV)
	if err != nil {
		log.Fatalf("targets: %v", err)
	}
	log.Printf("anomaly-smc over %d site(s), rho=%.3f", len(targets), *rho)

	byWeek := map[string][]member{}
	for _, tgt := range targets {
		site, err := siteload.Load(ctx, bw, hy, tgt.Notation, tgt.Lat, tgt.Long, tgt.Name, *dist, *window)
		if err != nil {
			continue
		}
		region := ""
		if len(site.Samples) > 0 {
			region = bwq.RegionOf(site.Samples[0].BathingWaterID)
		}
		for _, m := range siteMembers(site, *window, *threshold, region) {
			byWeek[m.key] = append(byWeek[m.key], m)
		}
	}

	// Leave-one-out within each region-week with ≥2 sampled sites.
	var withAnom, noAnom []forecast.Resolution
	groups, used := 0, 0
	for _, members := range byWeek {
		if len(members) < 2 {
			continue
		}
		groups++
		for bi, b := range members {
			if !b.det {
				continue // need a binary outcome to score the held-out site
			}
			// Condition on the other sites sampled this week.
			others := make([]anomaly.Obs, 0, len(members)-1)
			for j, o := range members {
				if j == bi {
					continue
				}
				others = append(others, obsOf(o, *rho))
			}
			post := anomaly.PosteriorZ(others, 0, 1, 1500, int64(bi+1))

			lamB := math.Sqrt(*rho) * b.sigma
			idioB := math.Sqrt(1-*rho) * b.sigma
			condMean := b.mu + lamB*post.Mean
			condStd := math.Sqrt(lamB*lamB*post.Var + idioB*idioB)
			pCond := normalCDF((condMean - logThr) / condStd)
			pBase := normalCDF((b.mu - logThr) / b.sigma)

			withAnom = append(withAnom, res(pCond, b.exceeded))
			noAnom = append(noAnom, res(pBase, b.exceeded))
			used++
		}
	}
	if used == 0 {
		log.Fatal("no leave-one-out forecasts produced")
	}
	report(*rho, groups, used, withAnom, noAnom)
}

func siteMembers(site forecast.Site, window int, threshold float64, region string) []member {
	type tagged struct {
		t   time.Time
		obs exceedance.CovObservation
		exc bool
		det bool
	}
	var rows []tagged
	var obs []exceedance.CovObservation
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
		cv := exceedance.CovObservation{LogValue: o.LogValue, Censoring: o.Censoring, Covars: []float64{rmm, math.Sin(ang), math.Cos(ang)}}
		exc, det := forecast.Exceeded(s.EColi, threshold)
		rows = append(rows, tagged{s.Time, cv, exc, det})
		obs = append(obs, cv)
	}
	if len(obs) < 40 {
		return nil
	}
	fit := exceedance.FitRegression(obs, 3)
	if fit.Sigma <= 0 {
		return nil
	}
	out := make([]member, 0, len(rows))
	for i, rrow := range rows {
		yr, wk := rrow.t.ISOWeek()
		out = append(out, member{
			key:       fmt.Sprintf("%s|%d|%d", region, yr, wk),
			mu:        fit.Mean(rrow.obs.Covars),
			sigma:     fit.Sigma,
			logValue:  obs[i].LogValue,
			censoring: obs[i].Censoring,
			exceeded:  rrow.exc,
			det:       rrow.det,
		})
	}
	return out
}

func obsOf(m member, rho float64) anomaly.Obs {
	return anomaly.Obs{
		ResidMean: m.mu,
		Loading:   math.Sqrt(rho) * m.sigma,
		Idio:      math.Sqrt(1-rho) * m.sigma,
		LogValue:  m.logValue,
		Censoring: m.censoring,
	}
}

func res(p float64, y bool) forecast.Resolution {
	return forecast.Resolution{Prediction: forecast.Prediction{PExceed: p}, Exceeded: y, Brier: forecast.Brier(p, y), LogLoss: forecast.LogLoss(p, y)}
}

func report(rho float64, groups, used int, withAnom, noAnom []forecast.Resolution) {
	a := forecast.Aggregate(withAnom)
	b := forecast.Aggregate(noAnom)
	fmt.Printf("\nAnomaly-SMC cross-site test — %d region-weeks, %d leave-one-out forecasts, %.1f%% exceeded (rho=%.3f)\n",
		groups, used, 100*a.BaseRate, rho)
	fmt.Printf("\n  %-22s %9s %9s\n", "forecast", "logloss", "Brier")
	fmt.Printf("  %-22s %9.4f %9.4f\n", "no anomaly", b.MeanLogLoss, b.MeanBrier)
	fmt.Printf("  %-22s %9.4f %9.4f\n", "+ inferred anomaly", a.MeanLogLoss, a.MeanBrier)
	fmt.Printf("\n  Δ logloss %+.4f (%+.1f%%)   Δ Brier %+.4f (%+.1f%%)   negative = anomaly helps\n",
		a.MeanLogLoss-b.MeanLogLoss, pct(a.MeanLogLoss, b.MeanLogLoss),
		a.MeanBrier-b.MeanBrier, pct(a.MeanBrier, b.MeanBrier))
}

func pct(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return 100 * (a - b) / b
}

const sqrt2 = 1.4142135623730951

func normalCDF(x float64) float64 { return 0.5 * math.Erfc(-x/sqrt2) }

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
