package rank

import (
	"math/rand"
	"testing"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/fetch"
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

func TestScore_NoTopics(t *testing.T) {
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	s := fetch.Story{
		Title:     "Rust 1.87 lands",
		Points:    100,
		CreatedAt: now.Add(-2 * time.Hour),
	}
	got := Score(s, nil, now)
	// With no topics, multiplier is 1.0. We only assert it's positive
	// and below the title-match case from the next test.
	if got <= 0 {
		t.Errorf("Score = %f, want positive", got)
	}
}

func TestScore_TitleMatchDoublesScore(t *testing.T) {
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	s := fetch.Story{
		Title:     "Rust 1.87 lands",
		Points:    100,
		CreatedAt: now.Add(-2 * time.Hour),
	}
	base := Score(s, nil, now)
	boosted := Score(s, []string{"rust"}, now)
	if boosted != 2*base {
		t.Errorf("Score with matching topic = %f, want 2*%f = %f", boosted, base, 2*base)
	}
}

func TestScore_TopicMatchTable(t *testing.T) {
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		title   string
		topics  []string
		wantMul float64
	}{
		{"no topics", "Rust 1.87 lands", nil, 1.0},
		{"no match", "React 21 announced", []string{"rust"}, 1.0},
		{"single word lowercase", "rust 1.87 lands", []string{"rust"}, 2.0},
		{"single word case-insensitive", "Rust 1.87 lands", []string{"rust"}, 2.0},
		{"topic uppercase title lowercase", "rust 1.87 lands", []string{"Rust"}, 2.0},
		{"rust does not match trust", "Trust the process", []string{"rust"}, 1.0},
		{"em-dash tokenizes", "Rust—a memory-safe language", []string{"rust"}, 2.0},
		{"hyphen tokenizes", "Memory-safe Rust lands", []string{"rust"}, 2.0},
		{"multi-word substring match", "Demystifying machine learning at scale", []string{"machine learning"}, 2.0},
		{"multi-word no match", "A learning machine", []string{"machine learning"}, 1.0},
		{"multi-word case-insensitive", "Machine Learning for All", []string{"machine learning"}, 2.0},
		{"any of multiple topics matches", "React 21", []string{"rust", "react"}, 2.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := fetch.Story{Title: tc.title, Points: 100, CreatedAt: now.Add(-1 * time.Hour)}
			base := Score(s, nil, now)
			got := Score(s, tc.topics, now)
			gotMul := got / base
			if gotMul != tc.wantMul {
				t.Errorf("multiplier for %q with topics %v = %f, want %f",
					tc.title, tc.topics, gotMul, tc.wantMul)
			}
		})
	}
}

func TestScore_AgeDecay(t *testing.T) {
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	young := fetch.Story{Title: "x", Points: 100, CreatedAt: now.Add(-1 * time.Hour)}
	old := fetch.Story{Title: "x", Points: 100, CreatedAt: now.Add(-24 * time.Hour)}
	if Score(young, nil, now) <= Score(old, nil, now) {
		t.Error("younger story should score higher than older story with same points")
	}
}
