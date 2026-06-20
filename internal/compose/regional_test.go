package compose

import (
	"math"
	"testing"
)

// TestAnomalyMeanReverts checks the OU partition carries temporal state: with no
// volatility it relaxes monotonically from its initial value toward the long-run
// mean, step by step — i.e. each step depends on the previous one.
func TestAnomalyMeanReverts(t *testing.T) {
	sites := []RegionalSite{{Name: "a", BaseMu: 2, Sigma: 1, Lambda: 1, LogThreshold: math.Log(500)}}
	run := RunRegional(sites, OUParams{Theta: 0.3, Sigma: 0, Mu: 0, Init: 3.0, Seed: 1}, 20)

	if len(run.Anomaly) != 20 {
		t.Fatalf("got %d anomaly steps, want 20", len(run.Anomaly))
	}
	prev := math.Inf(1)
	for i, z := range run.Anomaly {
		if z < 0 {
			t.Errorf("step %d: anomaly %.4f overshot the mean 0", i, z)
		}
		if z >= prev {
			t.Errorf("step %d: anomaly %.4f not decreasing toward mean (prev %.4f)", i, z, prev)
		}
		prev = z
	}
	if run.Anomaly[len(run.Anomaly)-1] > 0.1 {
		t.Errorf("anomaly should have relaxed near 0, ended at %.4f", run.Anomaly[len(run.Anomaly)-1])
	}
}

// TestRegionalCoherence checks the load-bearing coupling property: every site
// reads the SAME shared anomaly each step, so each site's anomaly contribution is
// exactly its loading times the shared z(t) — the sites move coherently.
func TestRegionalCoherence(t *testing.T) {
	sites := []RegionalSite{
		{Name: "strong", BaseMu: 1.5, Sigma: 2.0, Lambda: 1.0, LogThreshold: math.Log(500)},
		{Name: "weak", BaseMu: 1.5, Sigma: 2.0, Lambda: 0.4, LogThreshold: math.Log(500)},
		{Name: "inert", BaseMu: 1.5, Sigma: 2.0, Lambda: 0.0, LogThreshold: math.Log(500)},
	}
	run := RunRegional(sites, OUParams{Theta: 0.2, Sigma: 0.8, Mu: 0, Init: 0, Seed: 7}, 50)

	for i, z := range run.Anomaly {
		if d := math.Abs(run.AnomalyContrib["strong"][i] - 1.0*z); d > 1e-9 {
			t.Errorf("step %d: strong contrib %.4f != 1.0·z %.4f", i, run.AnomalyContrib["strong"][i], z)
		}
		if d := math.Abs(run.AnomalyContrib["weak"][i] - 0.4*z); d > 1e-9 {
			t.Errorf("step %d: weak contrib != 0.4·z", i)
		}
		// The inert site (λ=0) must be unmoved by the anomaly.
		if math.Abs(run.AnomalyContrib["inert"][i]) > 1e-12 {
			t.Errorf("step %d: inert site should ignore the anomaly, got %.4f", i, run.AnomalyContrib["inert"][i])
		}
	}

	// Coherence: strong and weak P(exceed) move in the same direction across steps.
	var agree, total int
	for i := 1; i < len(run.Anomaly); i++ {
		ds := run.PExceed["strong"][i] - run.PExceed["strong"][i-1]
		dw := run.PExceed["weak"][i] - run.PExceed["weak"][i-1]
		if ds*dw > 0 {
			agree++
		}
		total++
	}
	if agree != total {
		t.Errorf("loaded sites should always move together: agreed %d/%d steps", agree, total)
	}
}

// TestAnomalyRaisesRisk: a positive anomaly lifts a loaded site's exceedance
// probability above its no-anomaly baseline.
func TestAnomalyRaisesRisk(t *testing.T) {
	site := RegionalSite{Name: "s", BaseMu: 1.5, Sigma: 2.0, Lambda: 1.0, LogThreshold: math.Log(500)}
	base := normalCDF((site.BaseMu - site.LogThreshold) / site.Sigma)
	// Hold the anomaly pinned high (mean 3, no noise, start at 3).
	run := RunRegional([]RegionalSite{site}, OUParams{Theta: 1.0, Sigma: 0, Mu: 3, Init: 3, Seed: 1}, 5)
	for i, p := range run.PExceed["s"] {
		if p <= base {
			t.Errorf("step %d: high anomaly should raise P(exceed) above baseline %.4f, got %.4f", i, base, p)
		}
	}
}
