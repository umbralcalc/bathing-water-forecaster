package compose

import (
	"math"
	"math/rand"
	"testing"

	"github.com/umbralcalc/bathing-water-forecaster/internal/exceedance"
)

// TestComposedMatchesRegression is the reproduce milestone's load-bearing check:
// the stochadex partition composition must produce exactly the same μ and
// P(exceed) as the monolithic exceedance.Regression it is meant to replace.
func TestComposedMatchesRegression(t *testing.T) {
	c := Coeffs{Beta0: 1.8, BetaRain: 0.15, BetaSin: 0.4, BetaCos: -0.3, Sigma: 2.4, LogThreshold: math.Log(500)}
	reg := exceedance.Regression{Beta: []float64{c.Beta0, c.BetaRain, c.BetaSin, c.BetaCos}, Sigma: c.Sigma}

	r := rand.New(rand.NewSource(1))
	days := make([]DayInput, 60)
	for i := range days {
		ang := r.Float64() * 2 * math.Pi
		days[i] = DayInput{Rain: r.Float64() * 40, Sin: math.Sin(ang), Cos: math.Cos(ang)}
	}

	d := Run(c, days)
	if len(d.PExceed) != len(days) {
		t.Fatalf("got %d output rows, want %d (alignment/length mismatch)", len(d.PExceed), len(days))
	}

	for i, in := range days {
		wantMu := c.Beta0 + c.BetaRain*in.Rain + c.BetaSin*in.Sin + c.BetaCos*in.Cos
		wantP := reg.ExceedanceProb([]float64{in.Rain, in.Sin, in.Cos}, 500)

		if math.Abs(d.Mu[i]-wantMu) > 1e-9 {
			t.Errorf("day %d: composed μ %.6f != analytic %.6f", i, d.Mu[i], wantMu)
		}
		if math.Abs(d.PExceed[i]-wantP) > 1e-9 {
			t.Errorf("day %d: composed P(exceed) %.6f != regression %.6f", i, d.PExceed[i], wantP)
		}
		// The partition contributions must sum to μ — the decomposition is exact.
		if sum := d.Baseline[i] + d.Season[i] + d.Rainfall[i]; math.Abs(sum-d.Mu[i]) > 1e-9 {
			t.Errorf("day %d: baseline+season+rainfall %.6f != μ %.6f", i, sum, d.Mu[i])
		}
	}
}

// TestComponentsAttributable checks that each partition really carries its own
// term: zero rain ⇒ zero rainfall contribution, and the baseline is constant.
func TestComponentsAttributable(t *testing.T) {
	c := Coeffs{Beta0: 2.0, BetaRain: 0.1, BetaSin: 0.5, BetaCos: 0.2, Sigma: 2.0, LogThreshold: math.Log(500)}
	days := []DayInput{
		{Rain: 0, Sin: 0, Cos: 1},
		{Rain: 30, Sin: 1, Cos: 0},
	}
	d := Run(c, days)

	if math.Abs(d.Rainfall[0]) > 1e-12 {
		t.Errorf("dry day should have zero rainfall contribution, got %.6f", d.Rainfall[0])
	}
	if d.Rainfall[1] <= 0 {
		t.Errorf("wet day should have positive rainfall contribution, got %.6f", d.Rainfall[1])
	}
	for i := range days {
		if math.Abs(d.Baseline[i]-c.Beta0) > 1e-12 {
			t.Errorf("baseline should be constant β0=%.3f, got %.6f", c.Beta0, d.Baseline[i])
		}
	}
}
