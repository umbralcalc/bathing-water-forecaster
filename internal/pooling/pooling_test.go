package pooling

import (
	"math"
	"math/rand"
	"testing"
)

// TestPoolingReducesError is the load-bearing property: when site truths really
// are drawn from a shared distribution, partially-pooled estimates are closer to
// the truth than the independent ones — the Stein/borrowing-strength effect that
// justifies pooling. The guarantee holds in expectation, so the test averages the
// total squared error over many independent replications rather than betting on a
// single noisy draw, in a regime with genuinely sparse (data-poor) sites.
func TestPoolingReducesError(t *testing.T) {
	const (
		reps   = 60
		nSites = 120
		mu     = 3.0
		tau    = 0.8 // true between-site spread
	)
	var totRaw, totPool, sumMean, sumTau2 float64
	for rep := 0; rep < reps; rep++ {
		r := rand.New(rand.NewSource(int64(rep) + 1))
		var ests []Estimate
		truth := make([]float64, nSites)
		for i := 0; i < nSites; i++ {
			theta := mu + r.NormFloat64()*tau
			truth[i] = theta
			n := 3 + r.Intn(40)   // many sparse sites
			v := 4.0 / float64(n) // sampling variance comparable to τ² → real shrinkage
			y := theta + r.NormFloat64()*math.Sqrt(v)
			ests = append(ests, Estimate{Key: string(rune(i)), Value: y, Variance: v, N: n})
		}
		hyper, pooled := Pool(ests)
		sumMean += hyper.Mean
		sumTau2 += hyper.Tau2
		for i, p := range pooled {
			totRaw += (p.Raw - truth[i]) * (p.Raw - truth[i])
			totPool += (p.Pooled - truth[i]) * (p.Pooled - truth[i])
		}
	}
	t.Logf("avg fitted mean=%.3f (true %.1f), tau2=%.3f (true %.3f)", sumMean/reps, mu, sumTau2/reps, tau*tau)
	t.Logf("mean total squared error: raw=%.2f pooled=%.2f (%.0f%% reduction)",
		totRaw/reps, totPool/reps, 100*(1-totPool/totRaw))
	if totPool >= totRaw {
		t.Errorf("pooling should reduce error in expectation: raw=%.1f pooled=%.1f", totRaw/reps, totPool/reps)
	}
	if math.Abs(sumMean/reps-mu) > 0.1 {
		t.Errorf("mean not recovered on average: %.3f", sumMean/reps)
	}
	if math.Abs(sumTau2/reps-tau*tau) > 0.15 {
		t.Errorf("tau2 not recovered on average: %.3f (want %.3f)", sumTau2/reps, tau*tau)
	}
}

// TestSparseSitesShrinkMore checks the qualitative behaviour: a data-poor site
// (high variance) is pulled toward the group mean far more than a data-rich one.
func TestSparseSitesShrinkMore(t *testing.T) {
	ests := []Estimate{
		{Key: "rich", Value: 6.0, Variance: 0.01, N: 500}, // far from mean, but trusted
		{Key: "sparse", Value: 6.0, Variance: 2.0, N: 4},  // same estimate, barely any data
	}
	// Surround them with a tight cluster near 3 so the group mean sits well below 6.
	for i := 0; i < 20; i++ {
		ests = append(ests, Estimate{Key: "c", Value: 3.0, Variance: 0.05, N: 100})
	}
	_, pooled := Pool(ests)

	var rich, sparse Pooled
	for _, p := range pooled {
		switch p.Key {
		case "rich":
			rich = p
		case "sparse":
			sparse = p
		}
	}
	if !(sparse.Weight < rich.Weight) {
		t.Errorf("sparse site should have lower self-weight: sparse=%.3f rich=%.3f", sparse.Weight, rich.Weight)
	}
	// The sparse site should move much further toward the group than the rich one.
	if !(math.Abs(sparse.Pooled-sparse.Raw) > math.Abs(rich.Pooled-rich.Raw)) {
		t.Errorf("sparse site should shrink more: sparse Δ=%.3f rich Δ=%.3f",
			math.Abs(sparse.Pooled-sparse.Raw), math.Abs(rich.Pooled-rich.Raw))
	}
}

// TestIdenticalSitesFullyPool: when every estimate agrees, between-site variance
// is ~0 and all estimates pool to the common value.
func TestIdenticalSitesFullyPool(t *testing.T) {
	var ests []Estimate
	for i := 0; i < 10; i++ {
		ests = append(ests, Estimate{Key: "s", Value: 4.0, Variance: 0.1, N: 50})
	}
	h, pooled := Pool(ests)
	if h.Tau2 > 1e-3 {
		t.Errorf("identical estimates should give tau2≈0, got %.4f", h.Tau2)
	}
	for _, p := range pooled {
		if math.Abs(p.Pooled-4.0) > 1e-6 {
			t.Errorf("pooled value should be 4.0, got %.4f", p.Pooled)
		}
	}
}

func TestPoolByGroupSingletonPassesThrough(t *testing.T) {
	ests := []Estimate{
		{Key: "a", Group: "ukc", Value: 3.0, Variance: 0.1, N: 50},
		{Key: "b", Group: "ukc", Value: 3.4, Variance: 0.1, N: 50},
		{Key: "lone", Group: "ukg", Value: 9.0, Variance: 2.0, N: 3}, // singleton region
	}
	pooled := PoolByGroup(ests)
	for _, p := range pooled {
		if p.Key == "lone" && math.Abs(p.Pooled-9.0) > 1e-9 {
			t.Errorf("singleton group should pass through unchanged, got %.3f", p.Pooled)
		}
	}
	if len(pooled) != 3 {
		t.Errorf("expected 3 results, got %d", len(pooled))
	}
}
