package exceedance

import (
	"math"
	"math/rand"
	"testing"

	"github.com/umbralcalc/bathing-water-forecaster/internal/bwq"
)

// synthetic draws n latent log-counts from Normal(mu, sigma) and censors them at
// a lower reporting limit and an upper countable bound, mirroring how the lab
// feed produces "< lower" and "> upper" qualifiers. It returns the censored
// observations the model sees, a naive variant that substitutes each cap as if
// it were an exact reading, and the fraction left-censored.
func synthetic(seed int64, n int, mu, sigma, lower, upper float64) (obs, naive []Observation, fracLeft float64) {
	r := rand.New(rand.NewSource(seed))
	logLower, logUpper := math.Log(lower), math.Log(upper)
	var left int
	for i := 0; i < n; i++ {
		latent := r.NormFloat64()*sigma + mu
		switch {
		case latent < logLower:
			obs = append(obs, Observation{LogValue: logLower, Censoring: bwq.LessThan})
			naive = append(naive, Observation{LogValue: logLower, Censoring: bwq.Actual})
			left++
		case latent > logUpper:
			obs = append(obs, Observation{LogValue: logUpper, Censoring: bwq.GreaterThan})
			naive = append(naive, Observation{LogValue: logUpper, Censoring: bwq.Actual})
		default:
			obs = append(obs, Observation{LogValue: latent, Censoring: bwq.Actual})
			naive = append(naive, Observation{LogValue: latent, Censoring: bwq.Actual})
		}
	}
	return obs, naive, float64(left) / float64(n)
}

// TestRecoverKnownParameters is the load-bearing correctness gate: from heavily
// left-censored synthetic data, the censored MLE must recover the true mean and
// spread of the latent log-count.
func TestRecoverKnownParameters(t *testing.T) {
	const (
		trueMu    = 3.689 // ~ log(40) colonies/100ml
		trueSigma = 1.3
		lower     = 50.0   // reporting limit → ~57% left-censored at these params
		upper     = 1500.0 // upper countable bound
		n         = 8000
	)
	obs, _, fracLeft := synthetic(1, n, trueMu, trueSigma, lower, upper)

	if fracLeft < 0.4 {
		t.Fatalf("test misconfigured: only %.0f%% left-censored, want a heavy regime", 100*fracLeft)
	}
	t.Logf("left-censored fraction: %.1f%%", 100*fracLeft)

	fit := FitGaussian(obs)
	t.Logf("recovered mu=%.4f (true %.4f), sigma=%.4f (true %.4f)", fit.Mu, trueMu, fit.Sigma, trueSigma)

	if math.Abs(fit.Mu-trueMu) > 0.06 {
		t.Errorf("mu not recovered: got %.4f, want %.4f", fit.Mu, trueMu)
	}
	if math.Abs(fit.Sigma-trueSigma) > 0.06 {
		t.Errorf("sigma not recovered: got %.4f, want %.4f", fit.Sigma, trueSigma)
	}
}

// TestNaiveSubstitutionIsBiased pins the honesty figure: substituting each "<
// limit" reading with the limit value biases the fitted mean upward, and the
// censored fit is materially closer to the truth. This is the result cmd/
// censoring-ablation exists to quantify.
func TestNaiveSubstitutionIsBiased(t *testing.T) {
	const (
		trueMu    = 3.689
		trueSigma = 1.3
		lower     = 50.0
		upper     = 1500.0
		n         = 8000
	)
	obs, naive, _ := synthetic(2, n, trueMu, trueSigma, lower, upper)

	censored := FitGaussian(obs)
	naiveFit := FitGaussian(naive)
	t.Logf("censored mu=%.4f, naive mu=%.4f, true mu=%.4f", censored.Mu, naiveFit.Mu, trueMu)

	// Substituting the lower cap forces censored draws upward → mean biased high.
	if naiveFit.Mu <= trueMu {
		t.Errorf("expected naive substitution to bias mu upward, got naive=%.4f true=%.4f", naiveFit.Mu, trueMu)
	}
	// And the censored fit must be the better estimator.
	if math.Abs(censored.Mu-trueMu) >= math.Abs(naiveFit.Mu-trueMu) {
		t.Errorf("censored fit (err %.4f) should beat naive (err %.4f)",
			math.Abs(censored.Mu-trueMu), math.Abs(naiveFit.Mu-trueMu))
	}

	// The bias must also distort the published quantity — exceedance probability.
	const thr = 200.0
	truth := ExceedanceProb(trueMu, trueSigma, thr)
	gotC := censored.ExceedanceProb(thr)
	gotN := naiveFit.ExceedanceProb(thr)
	t.Logf("P(>%.0f): true=%.4f censored=%.4f naive=%.4f", thr, truth, gotC, gotN)
	if math.Abs(gotC-truth) >= math.Abs(gotN-truth) {
		t.Errorf("censored exceedance prob (err %.4f) should beat naive (err %.4f)",
			math.Abs(gotC-truth), math.Abs(gotN-truth))
	}
}

// TestExceedanceProbRecovered checks the published quantity directly: the fitted
// exceedance probability matches the analytic truth under the true parameters.
func TestExceedanceProbRecovered(t *testing.T) {
	const (
		trueMu    = 3.689
		trueSigma = 1.3
		lower     = 50.0
		upper     = 1500.0
	)
	obs, _, _ := synthetic(3, 8000, trueMu, trueSigma, lower, upper)
	fit := FitGaussian(obs)

	for _, thr := range []float64{100, 200, 500} {
		truth := ExceedanceProb(trueMu, trueSigma, thr)
		got := fit.ExceedanceProb(thr)
		if math.Abs(got-truth) > 0.02 {
			t.Errorf("P(>%.0f): got %.4f, want %.4f", thr, got, truth)
		}
	}
}

// TestUncensoredMatchesMoments sanity-checks the optimiser: with no censoring, the
// MLE coincides with the plain sample mean and (population) standard deviation.
func TestUncensoredMatchesMoments(t *testing.T) {
	r := rand.New(rand.NewSource(4))
	const trueMu, trueSigma, n = 5.0, 0.8, 5000
	var obs []Observation
	for i := 0; i < n; i++ {
		obs = append(obs, Observation{LogValue: r.NormFloat64()*trueSigma + trueMu, Censoring: bwq.Actual})
	}
	mean, sd := naiveMoments(obs)
	fit := FitGaussian(obs)
	if math.Abs(fit.Mu-mean) > 1e-3 {
		t.Errorf("uncensored MLE mu %.5f should match sample mean %.5f", fit.Mu, mean)
	}
	if math.Abs(fit.Sigma-sd) > 5e-3 {
		t.Errorf("uncensored MLE sigma %.5f should match sample sd %.5f", fit.Sigma, sd)
	}
}

// TestLogNormalCDFTailStable guards the numerics: log Φ(z) must stay finite and
// monotone deep into the left tail where Φ(z) underflows to zero.
func TestLogNormalCDFTailStable(t *testing.T) {
	prev := math.Inf(1)
	for z := -2.0; z >= -60.0; z -= 1.0 {
		v := logNormalCDF(z)
		if math.IsInf(v, 0) || math.IsNaN(v) {
			t.Fatalf("logNormalCDF(%.0f) = %v, want finite", z, v)
		}
		if v >= prev {
			t.Errorf("logNormalCDF not decreasing at z=%.0f: %.4f >= %.4f", z, v, prev)
		}
		prev = v
	}
	// Continuity across the tail-approximation switch at tailZ.
	jump := math.Abs(logNormalCDF(tailZ-1e-6) - logNormalCDF(tailZ+1e-6))
	if jump > 0.05 {
		t.Errorf("discontinuity at tail switch: jump %.4f", jump)
	}
}
