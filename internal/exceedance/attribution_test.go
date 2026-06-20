package exceedance

import (
	"math"
	"testing"
)

func approx(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

// TestAttributionSumsToForecast is the defining property: baseline plus every
// feature's contribution must equal the actual P(exceed), exactly.
func TestAttributionSumsToForecast(t *testing.T) {
	// μ = 2.0 + 0.12·rain + 0.4·sin + 0.3·cos, σ=1.0, threshold=200.
	reg := Regression{Beta: []float64{2.0, 0.12, 0.4, 0.3}, Sigma: 1.0}
	covars := []float64{25, 0.7, -0.3} // wet day, mid-season
	neutral := []float64{0, 0, 0}
	feats := []Feature{{"rain", []int{0}}, {"season", []int{1, 2}}}

	a := reg.AttributeExceedance(covars, neutral, feats, 200)

	sum := a.Baseline
	for _, c := range a.Contrib {
		sum += c
	}
	if !approx(sum, a.Total, 1e-12) {
		t.Errorf("baseline+contribs = %.6f, want Total %.6f", sum, a.Total)
	}
	// Total must equal the direct forecast.
	if !approx(a.Total, reg.ExceedanceProb(covars, 200), 1e-12) {
		t.Errorf("Total %.6f should match ExceedanceProb %.6f", a.Total, reg.ExceedanceProb(covars, 200))
	}
	// Baseline is the dry, average-season forecast.
	if !approx(a.Baseline, reg.ExceedanceProb([]float64{0, 0, 0}, 200), 1e-12) {
		t.Errorf("baseline mismatch")
	}
}

// TestSingleFeatureIsExact: with one feature, its contribution is just the
// difference from baseline (no ordering to average over).
func TestSingleFeatureIsExact(t *testing.T) {
	reg := Regression{Beta: []float64{1.5, 0.1}, Sigma: 1.2}
	covars := []float64{30}
	feats := []Feature{{"rain", []int{0}}}
	a := reg.AttributeExceedance(covars, []float64{0}, feats, 500)

	want := reg.ExceedanceProb([]float64{30}, 500) - reg.ExceedanceProb([]float64{0}, 500)
	if !approx(a.Contrib["rain"], want, 1e-12) {
		t.Errorf("rain contribution %.6f, want %.6f", a.Contrib["rain"], want)
	}
}

// TestRainContributionMonotone: more antecedent rain ⇒ a larger rain push.
func TestRainContributionMonotone(t *testing.T) {
	reg := Regression{Beta: []float64{1.0, 0.15, 0.2, 0.1}, Sigma: 1.0}
	neutral := []float64{0, 0, 0}
	feats := []Feature{{"rain", []int{0}}, {"season", []int{1, 2}}}

	prev := -1.0
	for _, rain := range []float64{0, 5, 10, 20, 40} {
		a := reg.AttributeExceedance([]float64{rain, 0.5, -0.2}, neutral, feats, 200)
		c := a.Contrib["rain"]
		if c < prev {
			t.Errorf("rain contribution not monotone at rain=%.0f: %.4f < %.4f", rain, c, prev)
		}
		prev = c
	}
}

// TestSymmetricFeaturesShareEqually: two features with identical effect get equal
// Shapley attribution (a basic fairness check on the decomposition).
func TestSymmetricFeaturesShareEqually(t *testing.T) {
	// Two covariates with the same coefficient and the same value.
	reg := Regression{Beta: []float64{0.5, 0.1, 0.1}, Sigma: 1.0}
	feats := []Feature{{"a", []int{0}}, {"b", []int{1}}}
	a := reg.AttributeExceedance([]float64{10, 10}, []float64{0, 0}, feats, 100)
	if !approx(a.Contrib["a"], a.Contrib["b"], 1e-12) {
		t.Errorf("symmetric features should share equally: a=%.6f b=%.6f", a.Contrib["a"], a.Contrib["b"])
	}
}
