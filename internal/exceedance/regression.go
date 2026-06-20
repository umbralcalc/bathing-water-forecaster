package exceedance

import (
	"math"

	"github.com/umbralcalc/bathing-water-forecaster/internal/bwq"
)

// CovObservation is a censored log-count reading together with the covariate
// vector that drives its latent mean (e.g. antecedent rainfall over one or more
// lag windows). The intercept is supplied by the fitter, so Covars holds only
// the explanatory variables.
type CovObservation struct {
	LogValue  float64
	Censoring bwq.Censoring
	Covars    []float64
}

// Regression is a censored Gaussian model whose latent log-count mean is linear
// in the covariates: log c ~ Normal(β0 + Σ_j βj·xj, σ). It is the single-site
// bridge between the intercept-only [GaussianFit] and the PLAN's hierarchical
// model — same censored likelihood, a structured mean. Every observation, censored
// or not, contributes through its proper density / CDF / survival term.
type Regression struct {
	Beta  []float64 // Beta[0] intercept; Beta[1:] covariate coefficients (raw scale)
	Sigma float64
	LogLk float64
	N     int
}

// Mean returns the latent log-count mean β0 + Σ βj·xj for a covariate vector.
func (r Regression) Mean(covars []float64) float64 {
	mu := r.Beta[0]
	for j, x := range covars {
		mu += r.Beta[j+1] * x
	}
	return mu
}

// ExceedanceProb is P(count > threshold) at the given covariates.
func (r Regression) ExceedanceProb(covars []float64, threshold float64) float64 {
	if threshold <= 0 {
		return 1
	}
	return normalCDF((r.Mean(covars) - math.Log(threshold)) / r.Sigma)
}

// CoefVariances returns the approximate sampling variance of each fitted
// coefficient (Beta[0] intercept, Beta[1:] covariate slopes), from the diagonal
// of the inverse observed-information matrix — the negative Hessian of the
// censored log-likelihood at the optimum, with σ held fixed. Working with the
// full Hessian (rather than one coefficient at a time) accounts for the
// correlation between the intercept and the slopes, and because censored
// observations contribute less curvature, heavily-censored sites correctly report
// larger variances — exactly the sites that should borrow most strength when these
// variances feed the pooling step.
func (r Regression) CoefVariances(obs []CovObservation) []float64 {
	nb := len(r.Beta)
	out := make([]float64, nb)
	if len(obs) == 0 {
		for i := range out {
			out[i] = math.Inf(1)
		}
		return out
	}
	ll := func(beta []float64) float64 {
		var s float64
		for i := range obs {
			mu := beta[0]
			for j, x := range obs[i].Covars {
				mu += beta[j+1] * x
			}
			s += logLikOne(Observation{LogValue: obs[i].LogValue, Censoring: obs[i].Censoring}, mu, r.Sigma)
		}
		return s
	}
	step := make([]float64, nb)
	for i := range step {
		step[i] = 1e-3 * (1 + math.Abs(r.Beta[i]))
	}
	// Negative Hessian by central finite differences.
	H := make([][]float64, nb)
	for i := range H {
		H[i] = make([]float64, nb)
	}
	for i := 0; i < nb; i++ {
		for j := i; j < nb; j++ {
			var d2 float64
			if i == j {
				d2 = (ll(bump(r.Beta, i, step[i])) - 2*ll(r.Beta) + ll(bump(r.Beta, i, -step[i]))) / (step[i] * step[i])
			} else {
				pp := bump2(r.Beta, i, step[i], j, step[j])
				pm := bump2(r.Beta, i, step[i], j, -step[j])
				mp := bump2(r.Beta, i, -step[i], j, step[j])
				mm := bump2(r.Beta, i, -step[i], j, -step[j])
				d2 = (ll(pp) - ll(pm) - ll(mp) + ll(mm)) / (4 * step[i] * step[j])
			}
			H[i][j] = -d2
			H[j][i] = -d2
		}
	}
	cov, ok := invertSym(H)
	if !ok {
		for i := range out {
			out[i] = math.Inf(1)
		}
		return out
	}
	for i := 0; i < nb; i++ {
		out[i] = cov[i][i]
		if out[i] < 0 {
			out[i] = math.Inf(1)
		}
	}
	return out
}

// InterceptVariance is the sampling variance of the fitted intercept β0.
func (r Regression) InterceptVariance(obs []CovObservation) float64 {
	return r.CoefVariances(obs)[0]
}

func bump(beta []float64, i int, h float64) []float64 {
	out := append([]float64(nil), beta...)
	out[i] += h
	return out
}

func bump2(beta []float64, i int, hi float64, j int, hj float64) []float64 {
	out := append([]float64(nil), beta...)
	out[i] += hi
	out[j] += hj
	return out
}

// invertSym inverts a symmetric matrix by Gauss–Jordan elimination with partial
// pivoting. It returns ok=false on a singular matrix.
func invertSym(A [][]float64) ([][]float64, bool) {
	n := len(A)
	m := make([][]float64, n)
	for i := range m {
		m[i] = make([]float64, 2*n)
		copy(m[i], A[i])
		m[i][n+i] = 1
	}
	for col := 0; col < n; col++ {
		piv := col
		for r := col + 1; r < n; r++ {
			if math.Abs(m[r][col]) > math.Abs(m[piv][col]) {
				piv = r
			}
		}
		if math.Abs(m[piv][col]) < 1e-14 {
			return nil, false
		}
		m[col], m[piv] = m[piv], m[col]
		d := m[col][col]
		for k := 0; k < 2*n; k++ {
			m[col][k] /= d
		}
		for r := 0; r < n; r++ {
			if r == col {
				continue
			}
			f := m[r][col]
			for k := 0; k < 2*n; k++ {
				m[r][k] -= f * m[col][k]
			}
		}
	}
	inv := make([][]float64, n)
	for i := range inv {
		inv[i] = append([]float64(nil), m[i][n:]...)
	}
	return inv, true
}

// FitRegression maximises the censored log-likelihood of a linear-mean model.
// Covariates are standardised internally for a well-conditioned optimisation and
// the coefficients are returned on the original scale. nCov is the covariate
// count; every observation's Covars slice must have that length.
func FitRegression(obs []CovObservation, nCov int) Regression {
	if len(obs) == 0 {
		return Regression{Beta: make([]float64, nCov+1)}
	}

	mean, sd := standardisers(obs, nCov)

	// Negative log-likelihood in standardised-covariate space. Parameters are
	// [a, b_1..b_nCov, logSigma]: a is the intercept on standardised covariates.
	negloglik := func(theta []float64) float64 {
		sigma := math.Exp(theta[nCov+1])
		var nll float64
		for i := range obs {
			mu := theta[0]
			for j := 0; j < nCov; j++ {
				mu += theta[j+1] * standardise(obs[i].Covars[j], mean[j], sd[j])
			}
			nll -= logLikOne(Observation{LogValue: obs[i].LogValue, Censoring: obs[i].Censoring}, mu, sigma)
		}
		return nll
	}

	mu0, sd0 := naiveMomentsCov(obs)
	if sd0 <= 0 {
		sd0 = 1
	}
	start := make([]float64, nCov+2)
	step := make([]float64, nCov+2)
	start[0], step[0] = mu0, sd0
	for j := 1; j <= nCov; j++ {
		step[j] = 0.5
	}
	start[nCov+1], step[nCov+1] = math.Log(sd0), 0.5

	best := nelderMead(negloglik, start, step, 4000, 1e-10)

	// Un-standardise: μ = a + Σ b_j (x_j − m_j)/s_j
	//                   = (a − Σ b_j m_j/s_j) + Σ (b_j/s_j) x_j.
	beta := make([]float64, nCov+1)
	beta[0] = best[0]
	for j := 0; j < nCov; j++ {
		if sd[j] == 0 {
			continue
		}
		bj := best[j+1] / sd[j]
		beta[j+1] = bj
		beta[0] -= bj * mean[j]
	}
	sigma := math.Exp(best[nCov+1])

	r := Regression{Beta: beta, Sigma: sigma, N: len(obs)}
	// Recompute log-likelihood on the raw scale as the reported optimum.
	var ll float64
	for i := range obs {
		ll += logLikOne(Observation{LogValue: obs[i].LogValue, Censoring: obs[i].Censoring}, r.Mean(obs[i].Covars), sigma)
	}
	r.LogLk = ll
	return r
}

// standardisers returns the per-covariate mean and standard deviation used to
// standardise the design matrix.
func standardisers(obs []CovObservation, nCov int) (mean, sd []float64) {
	mean = make([]float64, nCov)
	sd = make([]float64, nCov)
	n := float64(len(obs))
	for j := 0; j < nCov; j++ {
		for i := range obs {
			mean[j] += obs[i].Covars[j]
		}
		mean[j] /= n
	}
	for j := 0; j < nCov; j++ {
		for i := range obs {
			d := obs[i].Covars[j] - mean[j]
			sd[j] += d * d
		}
		sd[j] = math.Sqrt(sd[j] / n)
	}
	return mean, sd
}

func standardise(x, mean, sd float64) float64 {
	if sd == 0 {
		return 0
	}
	return (x - mean) / sd
}

func naiveMomentsCov(obs []CovObservation) (mean, sd float64) {
	n := float64(len(obs))
	for i := range obs {
		mean += obs[i].LogValue
	}
	mean /= n
	for i := range obs {
		d := obs[i].LogValue - mean
		sd += d * d
	}
	return mean, math.Sqrt(sd / n)
}
