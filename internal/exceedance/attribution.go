package exceedance

import "math/bits"

// Feature is a named group of covariate columns attributed together — e.g. the
// seasonal sine and cosine form one "season" feature, since attributing them
// separately would be meaningless to a reader.
type Feature struct {
	Name string
	Cols []int
}

// Attribution is the decomposition of a single P(exceedance) forecast into a
// baseline plus a contribution per feature.
type Attribution struct {
	Total      float64            // P(exceed) at the actual covariates
	Baseline   float64            // P(exceed) with every feature at its neutral value
	Contrib    map[string]float64 // each feature's push on P(exceed); they sum to Total−Baseline
	FeatureOrd []string           // feature names in input order, for stable display
}

// AttributeExceedance decomposes the exceedance probability at covars into the
// baseline (all features at their neutral value) plus each feature's Shapley
// contribution. Shapley values are used because P(exceed) is a nonlinear function
// of the linear predictor (through Φ), so a feature's effect depends on which
// others are already "on"; averaging over all orderings gives the unique
// attribution that is order-independent and sums exactly to the forecast:
//
//	Baseline + Σ_features Contrib == Total.
//
// neutral supplies the reference value of every covariate column (e.g. zero
// antecedent rain, an average-season day). With ≤ a handful of features the exact
// 2^n subset sum below is trivially cheap.
func (r Regression) AttributeExceedance(covars, neutral []float64, features []Feature, threshold float64) Attribution {
	n := len(features)

	// P(exceed) when the given subset of features is at its actual value and the
	// rest are at neutral.
	pOf := func(active uint) float64 {
		x := append([]float64(nil), neutral...)
		for i, f := range features {
			if active&(1<<uint(i)) != 0 {
				for _, c := range f.Cols {
					x[c] = covars[c]
				}
			}
		}
		return r.ExceedanceProb(x, threshold)
	}

	full := uint(1) << uint(n)
	pcache := make([]float64, full)
	for s := uint(0); s < full; s++ {
		pcache[s] = pOf(s)
	}

	contrib := make(map[string]float64, n)
	ord := make([]string, n)
	for i := 0; i < n; i++ {
		ord[i] = features[i].Name
		var phi float64
		for s := uint(0); s < full; s++ {
			if s&(1<<uint(i)) != 0 {
				continue // subsets that do not already contain feature i
			}
			k := bits.OnesCount(s)
			w := fact(k) * fact(n-k-1) / fact(n)
			phi += w * (pcache[s|(1<<uint(i))] - pcache[s])
		}
		contrib[features[i].Name] = phi
	}

	return Attribution{
		Total:      pcache[full-1],
		Baseline:   pcache[0],
		Contrib:    contrib,
		FeatureOrd: ord,
	}
}

func fact(k int) float64 {
	f := 1.0
	for k > 1 {
		f *= float64(k)
		k--
	}
	return f
}
