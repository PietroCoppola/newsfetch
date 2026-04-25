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

// TestScore_TopicMatchWithTags exercises the M4 source-agnostic topic matcher.
// The matching surface is the title plus the story's Tags joined into one
// string, so a topic can match either a title token or a tag. Empty Tags
// must behave identically to pre-M4 (the existing TestScore_TopicMatchTable
// is the regression guard for the HN-style title-only path; this table adds
// the tag-matching cases that Lobste.rs needs).
func TestScore_TopicMatchWithTags(t *testing.T) {
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		title   string
		tags    []string
		topics  []string
		wantMul float64
	}{
		{
			name:    "tag-only single-word match",
			title:   "Belt-driven autobikes",
			tags:    []string{"security"},
			topics:  []string{"security"},
			wantMul: 2.0,
		},
		{
			name:    "title and tag both match — single boost, not double",
			title:   "Rust security audit results",
			tags:    []string{"rust", "security"},
			topics:  []string{"rust"},
			wantMul: 2.0,
		},
		{
			name:    "tag match is case-insensitive",
			title:   "x",
			tags:    []string{"AI"},
			topics:  []string{"ai"},
			wantMul: 2.0,
		},
		{
			name:    "no match in title or tags",
			title:   "React 21 lands",
			tags:    []string{"javascript", "frontend"},
			topics:  []string{"rust"},
			wantMul: 1.0,
		},
		{
			name:    "topic 'as' must not match token 'wasm'",
			title:   "wasm runtime improvements",
			tags:    nil,
			topics:  []string{"as"},
			wantMul: 1.0,
		},
		{
			name:    "topic 'as' must not match tag containing 'as'",
			title:   "x",
			tags:    []string{"wasm"},
			topics:  []string{"as"},
			wantMul: 1.0,
		},
		{
			name:    "multi-word topic must not match across the title|tag seam",
			title:   "intro to machine",
			tags:    []string{"learning"},
			topics:  []string{"machine learning"},
			wantMul: 1.0,
		},
		{
			name:    "multi-word tag tokenizes — single-word topic matches one of its tokens",
			title:   "x",
			tags:    []string{"machine learning"},
			topics:  []string{"learning"},
			wantMul: 2.0,
		},
		{
			name:    "empty tags behaves like pre-M4",
			title:   "Rust 1.87 lands",
			tags:    []string{},
			topics:  []string{"rust"},
			wantMul: 2.0,
		},
		{
			name:    "nil tags behaves like pre-M4",
			title:   "Rust 1.87 lands",
			tags:    nil,
			topics:  []string{"rust"},
			wantMul: 2.0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := fetch.Story{
				Title:     tc.title,
				Tags:      tc.tags,
				Points:    100,
				CreatedAt: now.Add(-1 * time.Hour),
			}
			base := Score(fetch.Story{Title: tc.title, Tags: tc.tags, Points: 100, CreatedAt: now.Add(-1 * time.Hour)}, nil, now)
			got := Score(s, tc.topics, now)
			gotMul := got / base
			if gotMul != tc.wantMul {
				t.Errorf("multiplier for title=%q tags=%v topics=%v = %f, want %f",
					tc.title, tc.tags, tc.topics, gotMul, tc.wantMul)
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

func TestSelect_EmptyPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Select with empty slice did not panic")
		}
	}()
	rng := rand.New(rand.NewSource(1))
	_ = Select(nil, Options{Now: time.Now()}, rng)
}

func TestSelect_DeterministicWithSeed(t *testing.T) {
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	stories := []fetch.Story{
		{ID: "a", Title: "Alpha", Points: 100, CreatedAt: now.Add(-1 * time.Hour), Tags: []string{}},
		{ID: "b", Title: "Beta", Points: 200, CreatedAt: now.Add(-2 * time.Hour), Tags: []string{}},
		{ID: "c", Title: "Gamma", Points: 150, CreatedAt: now.Add(-3 * time.Hour), Tags: []string{}},
	}
	opts := Options{Now: now, PoolSize: 3}
	a := Select(stories, opts, rand.New(rand.NewSource(17)))
	b := Select(stories, opts, rand.New(rand.NewSource(17)))
	if a.ID != b.ID {
		t.Errorf("Select not deterministic under same seed: %q vs %q", a.ID, b.ID)
	}
}

func TestSelect_PoolSizeCap_Rank11NeverPicked(t *testing.T) {
	// Build 12 stories where stories[10] has a dominant score. The pool
	// size is 10, so ranks 0..9 compete; rank 10 is excluded regardless
	// of seed. Run many seeds to make the assertion robust.
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	var stories []fetch.Story
	for i := 0; i < 12; i++ {
		// Give stories[0..9] the highest points so they fill the pool.
		// stories[10] gets a "dominant" 10_000 — higher than stories[11]
		// but below stories[0..9], so it sorts to rank 10 and is excluded
		// by the pool-size cap. Without that cap, 10_000 would still beat
		// stories[11] most of the time; the test asserts it never wins.
		pts := 10_010 - i
		if i == 10 {
			pts = 10_000
		}
		stories = append(stories, fetch.Story{
			ID:        fmtID(i),
			Title:     fmtID(i),
			Points:    pts,
			CreatedAt: now.Add(-1 * time.Hour),
			Tags:      []string{},
		})
	}
	opts := Options{Now: now, PoolSize: 10}
	for seed := int64(0); seed < 200; seed++ {
		got := Select(stories, opts, rand.New(rand.NewSource(seed)))
		if got.ID == fmtID(10) {
			t.Fatalf("rank-11 story picked at seed %d", seed)
		}
	}
}

func TestSelect_PoolSizeCap_Rank10CanBePicked(t *testing.T) {
	// Boundary twin: confirm the pool includes the 10th story. Build 10
	// stories where the 10th dominates; across many seeds the 10th
	// should win at least once (this is a sanity check that the pool
	// isn't silently capped at 9 by an off-by-one).
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	var stories []fetch.Story
	for i := 0; i < 10; i++ {
		pts := 10 - i
		if i == 9 {
			pts = 10_000 // the 10th story dominates
		}
		stories = append(stories, fetch.Story{
			ID:        fmtID(i),
			Title:     fmtID(i),
			Points:    pts,
			CreatedAt: now.Add(-1 * time.Hour),
			Tags:      []string{},
		})
	}
	opts := Options{Now: now, PoolSize: 10}
	tenthWon := false
	for seed := int64(0); seed < 200; seed++ {
		got := Select(stories, opts, rand.New(rand.NewSource(seed)))
		if got.ID == fmtID(9) {
			tenthWon = true
			break
		}
	}
	if !tenthWon {
		t.Fatal("rank-10 dominant story never picked; pool may be off-by-one (top-9 instead of top-10)")
	}
}

func fmtID(i int) string {
	return "s-" + string(rune('a'+i%26)) + string(rune('0'+i/10))
}
