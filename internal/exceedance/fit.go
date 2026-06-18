package exceedance

import "math"

// GaussianFit is the maximum-likelihood Normal(Mu, Sigma) on the latent
// log-count, fitted with full respect for censoring.
type GaussianFit struct {
	Mu    float64
	Sigma float64
	LogLk float64 // log-likelihood at the optimum
	N     int     // observations used
}

// FitGaussian estimates Mu and Sigma by maximising the censored log-likelihood.
// Sigma is optimised on the log scale so it stays strictly positive. The starting
// point is the naive moments of the reported log-values (a deliberately biased
// but well-placed seed that the censored objective then corrects).
func FitGaussian(obs []Observation) GaussianFit {
	if len(obs) == 0 {
		return GaussianFit{}
	}

	mu0, sd0 := naiveMoments(obs)
	if sd0 <= 0 {
		sd0 = 1
	}

	negloglik := func(theta []float64) float64 {
		mu, sigma := theta[0], math.Exp(theta[1])
		return -LogLikelihood(obs, mu, sigma)
	}

	start := []float64{mu0, math.Log(sd0)}
	step := []float64{sd0, 0.5}
	best := nelderMead(negloglik, start, step, 2000, 1e-10)

	mu, sigma := best[0], math.Exp(best[1])
	return GaussianFit{
		Mu:    mu,
		Sigma: sigma,
		LogLk: LogLikelihood(obs, mu, sigma),
		N:     len(obs),
	}
}

// ExceedanceProb is the fitted probability that a fresh count exceeds threshold.
func (f GaussianFit) ExceedanceProb(threshold float64) float64 {
	return ExceedanceProb(f.Mu, f.Sigma, threshold)
}

// naiveMoments returns the mean and standard deviation of the reported log-values,
// ignoring censoring — used only to seed the optimiser.
func naiveMoments(obs []Observation) (mean, sd float64) {
	n := float64(len(obs))
	for _, o := range obs {
		mean += o.LogValue
	}
	mean /= n
	for _, o := range obs {
		d := o.LogValue - mean
		sd += d * d
	}
	sd = math.Sqrt(sd / n)
	return mean, sd
}
