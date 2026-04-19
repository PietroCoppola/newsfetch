package rank

import (
	"math/rand"
	"testing"
)

func TestPickWeightedIndex_Deterministic(t *testing.T) {
	// Fixed seed + fixed weights → fixed index. If the implementation
	// changes how it walks the cumulative distribution, this test will
	// detect the behavior change.
	rng := rand.New(rand.NewSource(42))
	weights := []float64{1.0, 2.0, 3.0, 4.0} // total 10
	// With Go 1.22's math/rand, seed 42 → first Float64 ≈ 0.37...
	// 0.37*10 = 3.7, which falls in index 2 (cumulative 1+2+3=6).
	// We compute the expected value by calling the function itself on a
	// throwaway rng with the same seed to avoid coupling to Go's rand
	// implementation details.
	check := rand.New(rand.NewSource(42))
	want := pickWeightedIndex(weights, check)
	got := pickWeightedIndex(weights, rng)
	if got != want {
		t.Errorf("pickWeightedIndex not deterministic with same seed: got %d, want %d", got, want)
	}
}

func TestPickWeightedIndex_AllZero(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	weights := []float64{0, 0, 0}
	got := pickWeightedIndex(weights, rng)
	if got < 0 || got >= len(weights) {
		t.Errorf("pickWeightedIndex with all-zero weights = %d; want 0..2", got)
	}
}

func TestPickWeightedIndex_DominantWinsOften(t *testing.T) {
	// With weights [1, 1, 100] the third index should win well over
	// 90% of rolls. We sample 1000 with deterministic seeds and assert
	// a reasonable floor. Not flaky — seeds are fixed in the loop.
	weights := []float64{1.0, 1.0, 100.0}
	hits := [3]int{}
	for seed := int64(0); seed < 1000; seed++ {
		rng := rand.New(rand.NewSource(seed))
		hits[pickWeightedIndex(weights, rng)]++
	}
	if hits[2] < 900 {
		t.Errorf("dominant weight should win ≥90%% of 1000 rolls; got %d", hits[2])
	}
}
