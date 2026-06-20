package exceedance

import (
	"math"
	"math/rand"
	"testing"

	"github.com/umbralcalc/bathing-water-forecaster/internal/bwq"
)

// syntheticReg draws censored observations from a known linear-mean model
// μ = beta0 + beta1·rain, with rain ~ Uniform(0, maxRain), censored at a lower
// reporting limit and an upper countable bound — the regression analogue of the
// intercept-only synthetic generator.
func syntheticReg(seed int64, n int, beta0, beta1, sigma, maxRain, lower, upper float64) []CovObservation {
	r := rand.New(rand.NewSource(seed))
	logLower, logUpper := math.Log(lower), math.Log(upper)
	obs := make([]CovObservation, 0, n)
	for i := 0; i < n; i++ {
		rain := r.Float64() * maxRain
		latent := beta0 + beta1*rain + r.NormFloat64()*sigma
		o := CovObservation{Covars: []float64{rain}}
		switch {
		case latent < logLower:
			o.LogValue, o.Censoring = logLower, bwq.LessThan
		case latent > logUpper:
			o.LogValue, o.Censoring = logUpper, bwq.GreaterThan
		default:
			o.LogValue, o.Censoring = latent, bwq.Actual
		}
		obs = append(obs, o)
	}
	return obs
}

// TestRecoverRegressionCoefficients is the regression counterpart of the
// load-bearing recovery gate: from heavily censored data the censored MLE must
// recover the intercept and the rainfall slope.
func TestRecoverRegressionCoefficients(t *testing.T) {
	const (
		beta0   = 3.0  // ~ log(20) baseline log-count
		beta1   = 0.07 // each mm of antecedent rain adds 0.07 to log-count
		sigma   = 0.9
		maxRain = 30.0
		lower   = 50.0
		upper   = 4000.0
		n       = 9000
	)
	obs := syntheticReg(1, n, beta0, beta1, sigma, maxRain, lower, upper)

	fit := FitRegression(obs, 1)
	t.Logf("recovered beta0=%.4f (true %.4f), beta1=%.4f (true %.4f), sigma=%.4f (true %.4f)",
		fit.Beta[0], beta0, fit.Beta[1], beta1, fit.Sigma, sigma)

	if math.Abs(fit.Beta[0]-beta0) > 0.12 {
		t.Errorf("intercept not recovered: got %.4f, want %.4f", fit.Beta[0], beta0)
	}
	if math.Abs(fit.Beta[1]-beta1) > 0.012 {
		t.Errorf("rain slope not recovered: got %.4f, want %.4f", fit.Beta[1], beta1)
	}
	if math.Abs(fit.Sigma-sigma) > 0.06 {
		t.Errorf("sigma not recovered: got %.4f, want %.4f", fit.Sigma, sigma)
	}
}

// TestExceedanceProbRisesWithRain checks the behavioural property the dashboard
// rainfall→exceedance explorer depends on: more antecedent rain ⇒ higher P(exceed).
func TestExceedanceProbRisesWithRain(t *testing.T) {
	obs := syntheticReg(2, 9000, 3.0, 0.07, 0.9, 30, 50, 4000)
	fit := FitRegression(obs, 1)

	const thr = 200.0
	dry := fit.ExceedanceProb([]float64{0}, thr)
	wet := fit.ExceedanceProb([]float64{25}, thr)
	t.Logf("P(>%.0f): dry=%.3f wet(25mm)=%.3f", thr, dry, wet)
	if !(wet > dry) {
		t.Errorf("exceedance prob should rise with rain: dry=%.3f wet=%.3f", dry, wet)
	}
	if dry < 0 || wet > 1 {
		t.Errorf("probabilities out of range: dry=%.3f wet=%.3f", dry, wet)
	}
}

// TestRegressionBeatsInterceptOnly confirms the rainfall term genuinely improves
// fit: when the truth depends on rain, the regression's log-likelihood exceeds
// the intercept-only fit on the same data.
func TestRegressionBeatsInterceptOnly(t *testing.T) {
	obs := syntheticReg(3, 9000, 3.0, 0.07, 0.9, 30, 50, 4000)

	reg := FitRegression(obs, 1)

	// Intercept-only fit on the same observations.
	plain := make([]Observation, len(obs))
	for i, o := range obs {
		plain[i] = Observation{LogValue: o.LogValue, Censoring: o.Censoring}
	}
	base := FitGaussian(plain)

	t.Logf("logLk: regression=%.1f intercept-only=%.1f", reg.LogLk, base.LogLk)
	if reg.LogLk <= base.LogLk {
		t.Errorf("regression logLk %.2f should exceed intercept-only %.2f", reg.LogLk, base.LogLk)
	}
}

// TestInterceptVarianceMatchesMeanSE checks the intercept-variance approximation
// against the textbook standard error of a mean: with uncensored data and no
// covariates, Var(β0) ≈ σ²/n.
func TestInterceptVarianceMatchesMeanSE(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	const trueMu, trueSigma, n = 4.0, 1.1, 4000
	cov := make([]CovObservation, n)
	for i := range cov {
		cov[i] = CovObservation{LogValue: r.NormFloat64()*trueSigma + trueMu, Censoring: bwq.Actual, Covars: []float64{}}
	}
	fit := FitRegression(cov, 0)
	got := fit.InterceptVariance(cov)
	want := fit.Sigma * fit.Sigma / float64(n)
	if math.Abs(got-want)/want > 0.05 {
		t.Errorf("intercept variance %.3e should match σ²/n %.3e", got, want)
	}
}

// TestSlopeVarianceMatchesOLS checks the Hessian-based slope variance against the
// textbook OLS result Var(β1) = σ²/Σ(xᵢ−x̄)² for uncensored data.
func TestSlopeVarianceMatchesOLS(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	const n = 5000
	const b0, b1, sigma = 2.0, 0.08, 0.9
	cov := make([]CovObservation, n)
	var sx, sxx float64
	xs := make([]float64, n)
	for i := range cov {
		x := r.Float64() * 30
		xs[i] = x
		sx += x
		y := b0 + b1*x + r.NormFloat64()*sigma
		cov[i] = CovObservation{LogValue: y, Censoring: bwq.Actual, Covars: []float64{x}}
	}
	xbar := sx / n
	for _, x := range xs {
		sxx += (x - xbar) * (x - xbar)
	}
	fit := FitRegression(cov, 1)
	got := fit.CoefVariances(cov)[1]
	want := fit.Sigma * fit.Sigma / sxx
	if math.Abs(got-want)/want > 0.08 {
		t.Errorf("slope variance %.3e should match σ²/Σ(x−x̄)² %.3e", got, want)
	}
}

// TestInterceptVarianceGrowsWithCensoring confirms the property pooling relies on:
// censoring inflates the intercept's variance relative to fully-observed data.
func TestInterceptVarianceGrowsWithCensoring(t *testing.T) {
	obs, _, frac := synthetic(8, 4000, 3.6, 1.2, 50, 1500)
	if frac < 0.4 {
		t.Fatalf("test needs heavy censoring, got %.2f", frac)
	}
	cov := make([]CovObservation, len(obs))
	uncensored := make([]CovObservation, len(obs))
	for i, o := range obs {
		cov[i] = CovObservation{LogValue: o.LogValue, Censoring: o.Censoring, Covars: []float64{}}
		uncensored[i] = CovObservation{LogValue: o.LogValue, Censoring: bwq.Actual, Covars: []float64{}}
	}
	vc := FitRegression(cov, 0).InterceptVariance(cov)
	vu := FitRegression(uncensored, 0).InterceptVariance(uncensored)
	if !(vc > vu) {
		t.Errorf("censored intercept variance %.3e should exceed uncensored %.3e", vc, vu)
	}
}

// TestRegressionWithNoCovariatesMatchesGaussian checks the degenerate case: with
// zero covariates the regression reduces to the intercept-only Gaussian fit.
func TestRegressionWithNoCovariatesMatchesGaussian(t *testing.T) {
	obs, _, _ := synthetic(4, 6000, 3.7, 1.2, 50, 1500)
	cov := make([]CovObservation, len(obs))
	for i, o := range obs {
		cov[i] = CovObservation{LogValue: o.LogValue, Censoring: o.Censoring, Covars: []float64{}}
	}
	reg := FitRegression(cov, 0)
	base := FitGaussian(obs)
	if math.Abs(reg.Beta[0]-base.Mu) > 1e-3 {
		t.Errorf("no-covariate regression intercept %.5f should match Gaussian mu %.5f", reg.Beta[0], base.Mu)
	}
	if math.Abs(reg.Sigma-base.Sigma) > 1e-3 {
		t.Errorf("no-covariate regression sigma %.5f should match Gaussian sigma %.5f", reg.Sigma, base.Sigma)
	}
}
