// Package exceedance implements the censored log-count measurement model at the
// heart of the bathing-water forecaster.
//
// Lab colony counts are modelled as log-normal: the latent log-concentration of
// a determinand is Gaussian, and the published threshold defines an exceedance
// cut on it. The decisive feature is that most observations are interval-
// censored — a "< 10" reading says only that the true count lies below the
// reporting limit, not that it equals 10. This package treats each observation
// according to its censoring:
//
//   - actual ("="): the latent log-count is observed; contributes a Normal
//     density term.
//   - lessThan ("<"): the latent log-count lies below log(limit); contributes a
//     CDF term, log Φ((log limit − μ)/σ).
//   - greaterThan (">"): the latent log-count lies above log(limit); contributes
//     a survival term, log(1 − Φ(...)).
//
// Collapsing a censored reading to its cap (the naive shortcut) biases every
// fitted parameter and therefore every threshold probability; cmd/censoring-
// ablation quantifies that cost, and TestNaiveSubstitutionIsBiased here pins the
// direction of the bias.
package exceedance

import (
	"math"

	"github.com/umbralcalc/bathing-water-forecaster/internal/bwq"
)

const (
	log2pi  = 1.8378770664093454836 // log(2π)
	sqrt2   = 1.4142135623730951
	tailZ   = -10.0 // below this z, use the Gaussian-tail asymptote for log Φ
	minDLog = 1e-12 // floor for log of a non-positive reported count
)

// Observation is a single determinand reading in log-count space: the natural
// log of the reported value, together with how that value is censored.
type Observation struct {
	LogValue  float64
	Censoring bwq.Censoring
}

// ObservationFromCount maps a decoded API count to a model observation, working
// in natural-log space. A non-positive "actual" count (none detected, reported
// as a bare 0) is treated as left-censored at a small positive floor rather than
// taking log of zero — the honest reading of "nothing grew".
func ObservationFromCount(c bwq.Count) (Observation, bool) {
	if !c.Present {
		return Observation{}, false
	}
	if c.Value <= 0 {
		return Observation{LogValue: math.Log(minDLog), Censoring: bwq.LessThan}, true
	}
	return Observation{LogValue: math.Log(c.Value), Censoring: c.Censoring}, true
}

// LogLikelihood returns the total log-likelihood of the observations under a
// Normal(mu, sigma) model on the latent log-count. sigma must be > 0.
func LogLikelihood(obs []Observation, mu, sigma float64) float64 {
	if sigma <= 0 {
		return math.Inf(-1)
	}
	var ll float64
	for _, o := range obs {
		ll += logLikOne(o, mu, sigma)
	}
	return ll
}

func logLikOne(o Observation, mu, sigma float64) float64 {
	z := (o.LogValue - mu) / sigma
	switch o.Censoring {
	case bwq.LessThan:
		return logNormalCDF(z) // log P(latent < limit)
	case bwq.GreaterThan:
		return logNormalCDF(-z) // log P(latent > limit) = log Φ(-z)
	default: // Actual
		return -0.5*z*z - math.Log(sigma) - 0.5*log2pi
	}
}

// ExceedanceProb is P(count > threshold) under Normal(mu, sigma) on the log-count,
// i.e. P(latent log-count > log threshold). threshold is a raw colony count.
func ExceedanceProb(mu, sigma, threshold float64) float64 {
	if threshold <= 0 {
		return 1
	}
	return normalCDF((mu - math.Log(threshold)) / sigma)
}

// normalCDF is the standard-normal CDF Φ(x), via the complementary error function.
func normalCDF(x float64) float64 {
	return 0.5 * math.Erfc(-x/sqrt2)
}

// logNormalCDF is log Φ(z), numerically stable into the far left tail where
// Φ(z) underflows to zero. Below tailZ it uses the leading asymptotic term
// log Φ(z) ≈ −z²/2 − log(2π)/2 − log(−z).
func logNormalCDF(z float64) float64 {
	if z > tailZ {
		return math.Log(0.5 * math.Erfc(-z/sqrt2))
	}
	return -0.5*z*z - 0.5*log2pi - math.Log(-z)
}
