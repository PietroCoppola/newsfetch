package rank_test

import (
	"math/rand"
	"strconv"
	"testing"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/fetch"
	"github.com/PietroCoppola/newsfetch/internal/rank"
)

func TestSelectN_ReturnsRequestedCount(t *testing.T) {
	stories := makeStories(10)
	got := mustSelectN(t, stories, 4, rank.Options{Now: refNow}, rand.New(rand.NewSource(1)))
	if len(got) != 4 {
		t.Errorf("len = %d, want 4", len(got))
	}
}

func TestSelectN_ReturnsFewerWhenPoolThin(t *testing.T) {
	stories := makeStories(2)
	got := mustSelectN(t, stories, 4, rank.Options{Now: refNow}, rand.New(rand.NewSource(1)))
	if len(got) != 2 {
		t.Errorf("len = %d, want 2 (pool was thin)", len(got))
	}
}

func TestSelectN_NoDuplicates(t *testing.T) {
	stories := makeStories(10)
	got := mustSelectN(t, stories, 4, rank.Options{Now: refNow}, rand.New(rand.NewSource(7)))
	seen := map[string]struct{}{}
	for _, s := range got {
		if _, dup := seen[s.ID]; dup {
			t.Fatalf("duplicate ID in result: %v", got)
		}
		seen[s.ID] = struct{}{}
	}
}

func TestSelectN_DiversityPrefersDifferentHost(t *testing.T) {
	// A clear setup: two pools, one all from same host and one from
	// different hosts but lower score. The diversity penalty should pull
	// the different-host story above the same-host one for slot 2.
	now := refNow
	stories := []fetch.Story{
		{ID: "a", URL: "https://same.com/1", Points: 1000, CreatedAt: now.Add(-time.Hour)},
		{ID: "b", URL: "https://same.com/2", Points: 900, CreatedAt: now.Add(-time.Hour)},
		{ID: "c", URL: "https://different.com/1", Points: 800, CreatedAt: now.Add(-time.Hour)},
	}
	// Seed 2's first Float64 (≈0.167) lands inside "a"'s weight share
	// (1000/2700 ≈ 0.370), making the stochastic hero deterministically
	// "a". The Fatalf below is a precondition guard, not an assertion: if
	// Score, the rng draw order, or these inputs ever change and the hero
	// shifts, the test fails loudly instead of silently skipping its
	// diversity assertion (which is what a plain `if hero == "a"` guard
	// did before — with seed 1 the hero was "b" and nothing was asserted).
	got := mustSelectN(t, stories, 2, rank.Options{Now: now, PoolSize: 3}, rand.New(rand.NewSource(2)))
	if got[0].ID != "a" {
		t.Fatalf("precondition: hero = %s, want a — seed/scoring drifted, re-pin the seed", got[0].ID)
	}
	// Slot 2 must prefer "c" over "b" despite "b"'s higher raw score:
	// b shares a's host (900 × 0.6 = 540) while c does not (800 × 1.0).
	if got[1].ID != "c" {
		t.Errorf("slot 2 = %s, want c (host-diverse over higher-scored same-host b)", got[1].ID)
	}
}

func TestSelectN_DiversityPrefersDifferentTags(t *testing.T) {
	now := refNow
	stories := []fetch.Story{
		{ID: "a", URL: "https://x.com/1", Points: 1000, Tags: []string{"rust"}, CreatedAt: now.Add(-time.Hour)},
		{ID: "b", URL: "https://y.com/1", Points: 950, Tags: []string{"rust"}, CreatedAt: now.Add(-time.Hour)},
		{ID: "c", URL: "https://z.com/1", Points: 700, Tags: []string{"go"}, CreatedAt: now.Add(-time.Hour)},
	}
	// Seed 2 pins the hero to "a" (same mechanics as the host test above);
	// the Fatalf keeps a future drift loud rather than silently vacuous.
	got := mustSelectN(t, stories, 2, rank.Options{Now: now, PoolSize: 3}, rand.New(rand.NewSource(2)))
	if got[0].ID != "a" {
		t.Fatalf("precondition: hero = %s, want a — seed/scoring drifted, re-pin the seed", got[0].ID)
	}
	// 950 × 0.4 (tag penalty) = 380 for b; 700 × 1.0 = 700 for c.
	if got[1].ID != "c" {
		t.Errorf("slot 2 = %s, want c (tag-diverse over higher-scored same-tag b)", got[1].ID)
	}
}

func TestSelectN_DiversityFallsBackWhenAllPenalised(t *testing.T) {
	// Every story shares both host and tags with every other. The penalty
	// fires uniformly so the original ranking should be preserved.
	now := refNow
	stories := []fetch.Story{
		{ID: "a", URL: "https://x.com/1", Points: 1000, Tags: []string{"t"}, CreatedAt: now.Add(-time.Hour)},
		{ID: "b", URL: "https://x.com/2", Points: 800, Tags: []string{"t"}, CreatedAt: now.Add(-time.Hour)},
		{ID: "c", URL: "https://x.com/3", Points: 600, Tags: []string{"t"}, CreatedAt: now.Add(-time.Hour)},
	}
	got := mustSelectN(t, stories, 3, rank.Options{Now: now, PoolSize: 3}, rand.New(rand.NewSource(1)))
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	// The hero is stochastic, but whoever it is, the two non-hero slots
	// carry identical diversity multipliers (every story shares host and
	// tag with every other), so their relative order must follow raw
	// score — descending Points. This holds for any hero, so no seed
	// pinning is needed.
	if got[1].Points < got[2].Points {
		t.Errorf("non-hero slots out of score order: got [%s(%d), %s(%d)], want descending Points",
			got[1].ID, got[1].Points, got[2].ID, got[2].Points)
	}
}

func TestSelectN_ErrorsOnEmpty(t *testing.T) {
	if _, err := rank.SelectN(nil, 1, rank.Options{Now: refNow}, rand.New(rand.NewSource(1))); err == nil {
		t.Error("expected error on empty input")
	}
}

func TestSelectN_ErrorsOnZeroN(t *testing.T) {
	if _, err := rank.SelectN(makeStories(3), 0, rank.Options{Now: refNow}, rand.New(rand.NewSource(1))); err == nil {
		t.Error("expected error on n <= 0")
	}
}

var refNow = time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)

// mustSelectN unwraps SelectN for tests whose inputs are valid by
// construction, keeping the assertions about selection, not plumbing.
func mustSelectN(t *testing.T, stories []fetch.Story, n int, opts rank.Options, rng *rand.Rand) []fetch.Story {
	t.Helper()
	got, err := rank.SelectN(stories, n, opts, rng)
	if err != nil {
		t.Fatalf("SelectN: %v", err)
	}
	return got
}

func makeStories(n int) []fetch.Story {
	out := make([]fetch.Story, n)
	for i := 0; i < n; i++ {
		id := strconv.Itoa(i)
		out[i] = fetch.Story{
			ID:        id,
			URL:       "https://host" + id + ".com/article",
			Source:    "hackernews",
			Points:    100 - i,
			CreatedAt: refNow.Add(-time.Hour),
		}
	}
	return out
}
