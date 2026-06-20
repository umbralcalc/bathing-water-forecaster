package anomaly

import (
	"math"
	"math/rand"
	"testing"

	"github.com/umbralcalc/bathing-water-forecaster/internal/bwq"
)

func corr(a, b []float64) float64 {
	n := float64(len(a))
	var ma, mb float64
	for i := range a {
		ma += a[i]
		mb += b[i]
	}
	ma, mb = ma/n, mb/n
	var sab, sa, sb float64
	for i := range a {
		da, db := a[i]-ma, b[i]-mb
		sab += da * db
		sa += da * da
		sb += db * db
	}
	return sab / math.Sqrt(sa*sb)
}

// TestFilterRecoversLatentTrajectory is the load-bearing check: from multi-site,
// partly-censored weekly observations the particle filter must track the hidden
// shared factor — its filtered mean correlates strongly with the true z(t) and
// beats the prior-mean-of-zero baseline.
func TestFilterRecoversLatentTrajectory(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	const (
		T      = 200
		phi    = 0.7
		nSites = 4
		mu     = 3.0 // per-site mean (log-count)
		sigma  = 1.0
		rho    = 0.4 // shared fraction → loading √rho·σ, idio √(1−rho)·σ
		cap    = 2.3 // log reporting limit (~10): values below are left-censored
	)
	loading := math.Sqrt(rho) * sigma
	idio := math.Sqrt(1-rho) * sigma

	trueZ := make([]float64, T)
	weeks := make([][]Obs, T)
	sd := math.Sqrt(1 - phi*phi)
	z := r.NormFloat64()
	for tt := 0; tt < T; tt++ {
		if tt > 0 {
			z = phi*z + sd*r.NormFloat64()
		}
		trueZ[tt] = z
		var wk []Obs
		for s := 0; s < nSites; s++ {
			y := mu + loading*z + idio*r.NormFloat64()
			o := Obs{ResidMean: mu, Loading: loading, Idio: idio, LogValue: y, Censoring: bwq.Actual}
			if y < cap { // emulate "< reporting limit" left-censoring
				o.LogValue, o.Censoring = cap, bwq.LessThan
			}
			wk = append(wk, o)
		}
		weeks[tt] = wk
	}

	res := Filter(weeks, phi, 2000, 42)
	est := make([]float64, T)
	for tt := range est {
		est[tt] = res.Z[tt].Mean
	}

	c := corr(est, trueZ)
	t.Logf("corr(filtered, true z) = %.3f", c)
	if c < 0.6 {
		t.Errorf("filter should track the latent factor, corr=%.3f", c)
	}

	// The filter must beat predicting z=0 everywhere.
	var rmseFilter, rmseZero float64
	for tt := range est {
		rmseFilter += (est[tt] - trueZ[tt]) * (est[tt] - trueZ[tt])
		rmseZero += trueZ[tt] * trueZ[tt]
	}
	if rmseFilter >= rmseZero {
		t.Errorf("filter RMSE %.3f should beat the zero baseline %.3f", math.Sqrt(rmseFilter/T), math.Sqrt(rmseZero/T))
	}
}

// TestPosteriorZSharpensWithSites: conditioning on more sites in a week shrinks
// the posterior variance of z below the prior.
func TestPosteriorZSharpensWithSites(t *testing.T) {
	loading, idio := math.Sqrt(0.4), math.Sqrt(0.6)
	mk := func(y float64) Obs {
		return Obs{ResidMean: 0, Loading: loading, Idio: idio, LogValue: y, Censoring: bwq.Actual}
	}
	one := PosteriorZ([]Obs{mk(0.8)}, 0, 1, 4000, 7)
	four := PosteriorZ([]Obs{mk(0.8), mk(0.6), mk(0.9), mk(0.7)}, 0, 1, 4000, 7)
	if !(four.Var < one.Var && one.Var < 1.0) {
		t.Errorf("more sites should sharpen posterior: prior=1.0, one=%.3f, four=%.3f", one.Var, four.Var)
	}
	// A cluster of high residuals should pull the posterior mean positive.
	if four.Mean <= 0 {
		t.Errorf("high residuals should push z positive, got %.3f", four.Mean)
	}
}

// TestEmptyWeekPropagates: a week with no observations leaves the posterior at
// the propagated prior (mean near 0, variance near 1 under the stationary prior).
func TestEmptyWeekPropagates(t *testing.T) {
	weeks := [][]Obs{nil, nil, nil}
	res := Filter(weeks, 0.5, 3000, 3)
	for i, p := range res.Z {
		if math.Abs(p.Mean) > 0.15 || math.Abs(p.Var-1) > 0.2 {
			t.Errorf("empty week %d: expected ~N(0,1), got mean=%.3f var=%.3f", i, p.Mean, p.Var)
		}
	}
}
