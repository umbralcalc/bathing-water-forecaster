package exceedance

import "math"

// nelderMead minimises f over an n-dimensional parameter vector using the
// downhill-simplex method. It is dependency-free and adequate for the smooth,
// low-dimensional likelihoods fitted here; heavier inference (the hierarchical
// model) will move to simulation-based methods, but the per-site Gaussian fit
// this supports is well-behaved enough for a direct optimiser.
//
// The implementation is the standard reflection / expansion / contraction /
// shrink scheme (Nelder & Mead 1965) with the common coefficients.
func nelderMead(f func([]float64) float64, start, step []float64, maxIter int, tol float64) []float64 {
	const (
		alpha = 1.0 // reflection
		gamma = 2.0 // expansion
		rho   = 0.5 // contraction
		sigma = 0.5 // shrink
	)
	n := len(start)

	// Build the initial simplex: the start point plus one vertex per axis.
	simplex := make([][]float64, n+1)
	vals := make([]float64, n+1)
	simplex[0] = append([]float64(nil), start...)
	vals[0] = f(simplex[0])
	for i := 0; i < n; i++ {
		v := append([]float64(nil), start...)
		v[i] += step[i]
		simplex[i+1] = v
		vals[i+1] = f(v)
	}

	centroid := make([]float64, n)
	for iter := 0; iter < maxIter; iter++ {
		order(simplex, vals)

		// Convergence: spread of function values across the simplex.
		if math.Abs(vals[n]-vals[0]) <= tol*(math.Abs(vals[0])+tol) {
			break
		}

		// Centroid of all but the worst vertex.
		for j := 0; j < n; j++ {
			centroid[j] = 0
			for i := 0; i < n; i++ {
				centroid[j] += simplex[i][j]
			}
			centroid[j] /= float64(n)
		}

		worst := simplex[n]
		reflected := axpy(centroid, alpha, centroid, worst)
		fr := f(reflected)

		switch {
		case fr < vals[0]:
			// Better than the best: try to expand further.
			expanded := axpy(centroid, gamma, centroid, worst)
			if fe := f(expanded); fe < fr {
				simplex[n], vals[n] = expanded, fe
			} else {
				simplex[n], vals[n] = reflected, fr
			}
		case fr < vals[n-1]:
			// Middling: accept the reflection.
			simplex[n], vals[n] = reflected, fr
		default:
			// Worse than the second-worst: contract.
			contracted := axpy(centroid, rho, worst, centroid)
			if fc := f(contracted); fc < vals[n] {
				simplex[n], vals[n] = contracted, fc
			} else {
				// Shrink the whole simplex toward the best vertex.
				best := simplex[0]
				for i := 1; i <= n; i++ {
					for j := 0; j < n; j++ {
						simplex[i][j] = best[j] + sigma*(simplex[i][j]-best[j])
					}
					vals[i] = f(simplex[i])
				}
			}
		}
	}
	order(simplex, vals)
	return simplex[0]
}

// axpy returns base + scale*(a - b), the affine combination Nelder–Mead uses for
// its reflection/expansion/contraction steps.
func axpy(base []float64, scale float64, a, b []float64) []float64 {
	out := make([]float64, len(base))
	for j := range base {
		out[j] = base[j] + scale*(a[j]-b[j])
	}
	return out
}

// order sorts the simplex vertices ascending by function value (best first).
func order(simplex [][]float64, vals []float64) {
	for i := 1; i < len(vals); i++ {
		for j := i; j > 0 && vals[j] < vals[j-1]; j-- {
			vals[j], vals[j-1] = vals[j-1], vals[j]
			simplex[j], simplex[j-1] = simplex[j-1], simplex[j]
		}
	}
}
