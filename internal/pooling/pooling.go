// Package pooling implements partial pooling of per-site parameter estimates by
// empirical-Bayes normal–normal shrinkage — the mechanism by which a sparse or
// PRF-skipped bathing water borrows strength from its region and the nation,
// while a well-sampled problem beach keeps its own signal.
//
// The model is the classic two-level normal hierarchy: each site's independent
// estimate y_i has sampling variance v_i, and the true site parameters θ_i are
// drawn from a shared distribution N(μ, τ²). Given μ and τ² (fitted by marginal
// maximum likelihood), the posterior mean shrinks each estimate toward μ by a
// weight w_i = τ²/(τ² + v_i): a site with little data (large v_i) shrinks hard
// toward the group; a site with lots of data (small v_i) barely moves. When τ²
// is large relative to the v_i the group is heterogeneous and pooling is light;
// when it is small the sites are alike and pooling is heavy.
package pooling

import "math"

// Estimate is one site's independent estimate of a scalar parameter, with the
// sampling variance of that estimate and an optional group key for grouped
// pooling.
type Estimate struct {
	Key      string
	Group    string
	Value    float64
	Variance float64
	N        int
}

// Hyper holds the fitted hierarchy hyperparameters.
type Hyper struct {
	Mean float64 // μ, the pooled-toward group mean
	Tau2 float64 // τ², the between-site variance
}

// Pooled is the shrinkage result for one site.
type Pooled struct {
	Key    string
	Group  string
	Raw    float64 // the independent estimate
	Pooled float64 // the partially-pooled estimate
	Weight float64 // w_i ∈ [0,1]: 1 = trust own data, 0 = fully pooled to the group
	Target float64 // the mean it shrank toward
	N      int
}

// Pool fits the normal–normal hierarchy over a flat set of estimates and returns
// the hyperparameters and each site's shrinkage. With fewer than two estimates
// there is nothing to pool toward, so each is returned unchanged.
func Pool(ests []Estimate) (Hyper, []Pooled) {
	if len(ests) == 0 {
		return Hyper{}, nil
	}
	if len(ests) == 1 {
		e := ests[0]
		return Hyper{Mean: e.Value, Tau2: 0}, []Pooled{{
			Key: e.Key, Group: e.Group, Raw: e.Value, Pooled: e.Value,
			Weight: 1, Target: e.Value, N: e.N,
		}}
	}

	y := make([]float64, len(ests))
	v := make([]float64, len(ests))
	for i, e := range ests {
		y[i] = e.Value
		v[i] = math.Max(e.Variance, 1e-12)
	}
	mean, tau2 := fitHierarchy(y, v)

	out := make([]Pooled, len(ests))
	for i, e := range ests {
		w := tau2 / (tau2 + v[i])
		out[i] = Pooled{
			Key:    e.Key,
			Group:  e.Group,
			Raw:    e.Value,
			Pooled: w*e.Value + (1-w)*mean,
			Weight: w,
			Target: mean,
			N:      e.N,
		}
	}
	return Hyper{Mean: mean, Tau2: tau2}, out
}

// PoolByGroup pools the estimates within each group separately (e.g. shrink each
// region's sites toward their own regional mean). Groups with a single site pass
// through unchanged — a singleton cannot borrow within its own group, which is
// the case nested national pooling exists to cover.
func PoolByGroup(ests []Estimate) []Pooled {
	order := []string{}
	byGroup := map[string][]Estimate{}
	for _, e := range ests {
		if _, ok := byGroup[e.Group]; !ok {
			order = append(order, e.Group)
		}
		byGroup[e.Group] = append(byGroup[e.Group], e)
	}
	var out []Pooled
	for _, g := range order {
		_, pooled := Pool(byGroup[g])
		out = append(out, pooled...)
	}
	return out
}

// fitHierarchy estimates μ and τ² by maximising the marginal likelihood
//
//	y_i ~ N(μ, v_i + τ²),   μ(τ²) = Σ y_i/(v_i+τ²) / Σ 1/(v_i+τ²)
//
// over τ² ≥ 0. The marginal log-likelihood in τ² is smooth and unimodal here, so
// a coarse grid followed by a golden-section refine finds the optimum robustly,
// including the τ²=0 (full-pooling) boundary.
func fitHierarchy(y, v []float64) (mean, tau2 float64) {
	// Upper bound: total spread of the estimates is an ample ceiling for τ².
	ybar := mean1(y)
	var spread, maxv float64
	for i := range y {
		spread += (y[i] - ybar) * (y[i] - ybar)
		if v[i] > maxv {
			maxv = v[i]
		}
	}
	spread /= float64(len(y))
	hi := spread + maxv + 1e-9

	obj := func(t2 float64) float64 { ll, _ := marginalLL(y, v, t2); return ll }

	// Coarse grid (includes the τ²=0 boundary), then golden-section refine.
	const grid = 64
	bestT, bestLL := 0.0, math.Inf(-1)
	for k := 0; k <= grid; k++ {
		t2 := hi * float64(k) / float64(grid)
		if ll := obj(t2); ll > bestLL {
			bestLL, bestT = ll, t2
		}
	}
	step := hi / float64(grid)
	lo := math.Max(0, bestT-step)
	tau2 = goldenMax(obj, lo, bestT+step)
	if obj(0) >= obj(tau2) {
		tau2 = 0
	}
	_, mean = marginalLL(y, v, tau2)
	return mean, tau2
}

func marginalLL(y, v []float64, tau2 float64) (ll, mean float64) {
	var sw, swy float64
	for i := range y {
		w := 1 / (v[i] + tau2)
		sw += w
		swy += w * y[i]
	}
	mean = swy / sw
	for i := range y {
		s := v[i] + tau2
		ll += -0.5 * (math.Log(2*math.Pi*s) + (y[i]-mean)*(y[i]-mean)/s)
	}
	return ll, mean
}

func goldenMax(f func(float64) float64, lo, hi float64) float64 {
	const invphi = 0.6180339887498949
	a, b := lo, hi
	c := b - (b-a)*invphi
	d := a + (b-a)*invphi
	fc, fd := f(c), f(d)
	for i := 0; i < 80 && b-a > 1e-10; i++ {
		if fc > fd {
			b, d, fd = d, c, fc
			c = b - (b-a)*invphi
			fc = f(c)
		} else {
			a, c, fc = c, d, fd
			d = a + (b-a)*invphi
			fd = f(d)
		}
	}
	return (a + b) / 2
}

func mean1(x []float64) float64 {
	var s float64
	for _, v := range x {
		s += v
	}
	return s / float64(len(x))
}
