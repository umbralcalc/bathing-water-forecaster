// Package anomaly infers the shared regional "wet-week" factor by sequential
// Monte Carlo — a bootstrap particle filter over the latent process z(t).
//
// A particle filter (rather than a closed-form Kalman update) is required because
// the observations are censored: most weekly counts are "< reporting limit", so a
// site contributes a CDF/survival term, not a Gaussian point, and the posterior
// over z is no longer Gaussian. Each site sampled in a week is modelled as
//
//	y = ResidMean + Loading·z + N(0, Idio),
//
// where ResidMean is the per-site rain+season mean μ, Loading=λ is the site's
// pull from the shared factor, and Idio is the leftover idiosyncratic scale, with
// λ²+Idio² = σ². The filter propagates z as a stationary AR(1) (z_t = φ·z_{t−1} +
// √(1−φ²)·η) and reweights particles by the censored likelihood of the week's
// observations.
//
// stochadex's own SMC module targets parameter inference; latent-state filtering
// is this focused filter, built on the same censored likelihood as
// internal/exceedance.
package anomaly

import (
	"math"
	"math/rand"

	"github.com/umbralcalc/bathing-water-forecaster/internal/bwq"
)

// Obs is one site's observation within a week.
type Obs struct {
	ResidMean float64 // μ: the per-site rain+season mean (log-count)
	Loading   float64 // λ: pull from the shared factor
	Idio      float64 // idiosyncratic scale (log-count)
	LogValue  float64 // log of the reported count (the cap, if censored)
	Censoring bwq.Censoring
}

// Posterior is the filtered estimate of z for one week.
type Posterior struct {
	Mean float64
	Var  float64
}

// FilterResult is the per-week filtered posterior of z(t).
type FilterResult struct {
	Z []Posterior
}

// Filter runs the bootstrap particle filter across weeks (each a slice of the
// sites observed that week; empty weeks are allowed and simply propagate). It
// returns the filtered posterior of z for every week.
func Filter(weeks [][]Obs, phi float64, nParticles int, seed int64) FilterResult {
	r := rand.New(rand.NewSource(seed))
	particles := make([]float64, nParticles)
	for i := range particles {
		particles[i] = r.NormFloat64() // stationary prior N(0,1)
	}
	out := FilterResult{Z: make([]Posterior, len(weeks))}
	sd := math.Sqrt(1 - phi*phi)
	for t, week := range weeks {
		if t > 0 {
			for i := range particles {
				particles[i] = phi*particles[i] + sd*r.NormFloat64()
			}
		}
		post, resampled := updateWeek(particles, week, r)
		out.Z[t] = post
		particles = resampled
	}
	return out
}

// PosteriorZ updates a single week's z from a Gaussian prior N(priorMean,
// priorVar) using the week's observations — the contemporaneous cross-site
// update used to condition one site's forecast on the others sampled that week.
func PosteriorZ(week []Obs, priorMean, priorVar float64, nParticles int, seed int64) Posterior {
	r := rand.New(rand.NewSource(seed))
	particles := make([]float64, nParticles)
	sd := math.Sqrt(math.Max(priorVar, 1e-12))
	for i := range particles {
		particles[i] = priorMean + sd*r.NormFloat64()
	}
	post, _ := updateWeek(particles, week, r)
	return post
}

// updateWeek reweights particles by the week's censored likelihood, returns the
// weighted posterior mean/variance, and systematically resamples to equal-weight
// particles for the next step. With no observations the week is uninformative and
// the particles pass through unchanged.
func updateWeek(particles []float64, week []Obs, r *rand.Rand) (Posterior, []float64) {
	n := len(particles)
	if len(week) == 0 {
		return weightedMoments(particles, nil), particles
	}
	logw := make([]float64, n)
	maxlw := math.Inf(-1)
	for i, z := range particles {
		var lw float64
		for _, o := range week {
			lw += censoredLogLik(o, z)
		}
		logw[i] = lw
		if lw > maxlw {
			maxlw = lw
		}
	}
	w := make([]float64, n)
	var sum float64
	for i := range w {
		w[i] = math.Exp(logw[i] - maxlw)
		sum += w[i]
	}
	if sum == 0 { // degenerate; treat as uninformative
		return weightedMoments(particles, nil), particles
	}
	for i := range w {
		w[i] /= sum
	}
	post := weightedMoments(particles, w)
	return post, systematicResample(particles, w, r)
}

func weightedMoments(particles []float64, w []float64) Posterior {
	n := len(particles)
	var mean float64
	if w == nil {
		for _, z := range particles {
			mean += z
		}
		mean /= float64(n)
		var v float64
		for _, z := range particles {
			v += (z - mean) * (z - mean)
		}
		return Posterior{Mean: mean, Var: v / float64(n)}
	}
	for i, z := range particles {
		mean += w[i] * z
	}
	var v float64
	for i, z := range particles {
		v += w[i] * (z - mean) * (z - mean)
	}
	return Posterior{Mean: mean, Var: v}
}

// systematicResample draws n equal-weight particles from the weighted set.
func systematicResample(particles, w []float64, r *rand.Rand) []float64 {
	n := len(particles)
	out := make([]float64, n)
	start := r.Float64() / float64(n)
	cum := w[0]
	j := 0
	for i := 0; i < n; i++ {
		u := start + float64(i)/float64(n)
		for u > cum && j < n-1 {
			j++
			cum += w[j]
		}
		out[i] = particles[j]
	}
	return out
}

// censoredLogLik is log p(observation | z): a Gaussian density for an actual
// count, and the matching tail probability for a censored one.
func censoredLogLik(o Obs, z float64) float64 {
	mean := o.ResidMean + o.Loading*z
	if o.Idio <= 0 {
		return 0
	}
	zscore := (o.LogValue - mean) / o.Idio
	switch o.Censoring {
	case bwq.LessThan:
		return logNormalCDF(zscore)
	case bwq.GreaterThan:
		return logNormalCDF(-zscore)
	default:
		return -0.5*zscore*zscore - math.Log(o.Idio) - 0.5*math.Log(2*math.Pi)
	}
}

const sqrt2 = 1.4142135623730951

func logNormalCDF(x float64) float64 {
	if x > -10 {
		return math.Log(0.5 * math.Erfc(-x/sqrt2))
	}
	return -0.5*x*x - 0.5*math.Log(2*math.Pi) - math.Log(-x)
}
