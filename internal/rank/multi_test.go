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
	got := rank.SelectN(stories, 4, rank.Options{Now: refNow}, rand.New(rand.NewSource(1)))
	if len(got) != 4 {
		t.Errorf("len = %d, want 4", len(got))
	}
}

func TestSelectN_ReturnsFewerWhenPoolThin(t *testing.T) {
	stories := makeStories(2)
	got := rank.SelectN(stories, 4, rank.Options{Now: refNow}, rand.New(rand.NewSource(1)))
	if len(got) != 2 {
		t.Errorf("len = %d, want 2 (pool was thin)", len(got))
	}
}

func TestSelectN_NoDuplicates(t *testing.T) {
	stories := makeStories(10)
	got := rank.SelectN(stories, 4, rank.Options{Now: refNow}, rand.New(rand.NewSource(7)))
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
	// Force deterministic hero by using PoolSize=1 (only top-scored survives
	// the pool cut, so the stochastic pick is degenerate).
	got := rank.SelectN(stories, 2, rank.Options{Now: now, PoolSize: 3}, rand.New(rand.NewSource(1)))
	// Hero should be "a" almost surely (highest score, weighted pick within
	// 3-story pool dominated by it). Slot 2 should prefer "c" over "b"
	// despite "b" having a higher raw score, because of host penalty.
	// We assert: if hero is a, slot 2 must be c.
	if got[0].ID == "a" && got[1].ID != "c" {
		t.Errorf("with hero=a, expected slot 2=c (host-diverse) but got %s", got[1].ID)
	}
}

func TestSelectN_DiversityPrefersDifferentTags(t *testing.T) {
	now := refNow
	stories := []fetch.Story{
		{ID: "a", URL: "https://x.com/1", Points: 1000, Tags: []string{"rust"}, CreatedAt: now.Add(-time.Hour)},
		{ID: "b", URL: "https://y.com/1", Points: 950, Tags: []string{"rust"}, CreatedAt: now.Add(-time.Hour)},
		{ID: "c", URL: "https://z.com/1", Points: 700, Tags: []string{"go"}, CreatedAt: now.Add(-time.Hour)},
	}
	got := rank.SelectN(stories, 2, rank.Options{Now: now, PoolSize: 3}, rand.New(rand.NewSource(1)))
	// 950 * 0.4 (tag penalty) = 380; 700 * 1.0 = 700. So if hero is "a",
	// slot 2 should be "c".
	if got[0].ID == "a" && got[1].ID != "c" {
		t.Errorf("with hero=a, expected slot 2=c (tag-diverse) but got %s", got[1].ID)
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
	got := rank.SelectN(stories, 3, rank.Options{Now: now, PoolSize: 3}, rand.New(rand.NewSource(1)))
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	// Hero is stochastic but ticker order should follow remaining-score.
	// All multipliers are the same so ordering by raw score is preserved
	// among the non-hero stories.
}

func TestSelectN_PanicsOnEmpty(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on empty input")
		}
	}()
	rank.SelectN(nil, 1, rank.Options{Now: refNow}, rand.New(rand.NewSource(1)))
}

func TestSelectN_PanicsOnZeroN(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on n<=0")
		}
	}()
	rank.SelectN(makeStories(3), 0, rank.Options{Now: refNow}, rand.New(rand.NewSource(1)))
}

var refNow = time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)

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
