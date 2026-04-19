// Package rank scores cached stories and picks one via weighted stochastic
// selection. All exported functions are free of global state; Select takes
// an injected *rand.Rand so callers (main + tests) control determinism.
package rank

import (
	"math/rand"
)

// pickWeightedIndex returns an index i into weights, chosen with probability
// proportional to weights[i]. If every weight is <= 0 the caller almost
// certainly has a bug; we pick uniformly to avoid a panic. Weights with
// value <= 0 never win (treated as zero).
func pickWeightedIndex(weights []float64, rng *rand.Rand) int {
	total := 0.0
	for _, w := range weights {
		if w > 0 {
			total += w
		}
	}
	if total == 0 {
		return rng.Intn(len(weights))
	}
	r := rng.Float64() * total
	acc := 0.0
	for i, w := range weights {
		if w <= 0 {
			continue
		}
		acc += w
		if r < acc {
			return i
		}
	}
	return len(weights) - 1
}
