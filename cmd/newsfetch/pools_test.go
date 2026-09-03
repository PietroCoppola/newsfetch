package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/cache"
	"github.com/PietroCoppola/newsfetch/internal/config"
	"github.com/PietroCoppola/newsfetch/internal/defaults"
	"github.com/PietroCoppola/newsfetch/internal/feedstate"
	"github.com/PietroCoppola/newsfetch/internal/fetch"
	"github.com/PietroCoppola/newsfetch/internal/render"
)

// poolTestCfg is a fully-populated two-pool config: news with one
// aggregator, following with two feeds, following first in pool order.
// Built by hand rather than through config.Load so the selection tests
// exercise the pipeline, not the TOML decoder.
func poolTestCfg() config.Config {
	return config.Config{
		Topics:         nil,
		Style:          "boxed",
		CacheTTL:       30 * time.Minute,
		MinPoints:      defaults.MinPoints,
		Count:          1,
		FollowingCount: 1,
		Pools:          []string{"news", "following"},
		PoolOrder:      []string{"following", "news"},
		News:           config.NewsConfig{Aggregators: []string{"hackernews"}},
		Following: config.FollowingConfig{Feeds: []config.FeedConfig{
			{URL: "https://a.example/feed.xml"},
			{URL: "https://b.example/feed.xml"},
		}},
		TickerMarker: defaults.TickerMarker,
		TickerBoxed:  defaults.TickerBoxed,
		DedupWindow:  defaults.DedupWindow,
	}
}

// poolTestStories returns n aggregator stories with descending points and
// distinct hosts, so neither the diversity penalty nor a host collision
// muddies a count assertion.
func poolTestStories(now time.Time, n int) []fetch.Story {
	out := make([]fetch.Story, 0, n)
	for i := range n {
		out = append(out, fetch.Story{
			ID:        string(rune('a' + i)),
			Title:     "Story " + string(rune('A'+i)),
			URL:       "https://host" + string(rune('a'+i)) + ".example/x",
			Source:    "hackernews",
			Points:    100 - i,
			CreatedAt: now.Add(-time.Duration(i+1) * time.Hour),
			Tags:      []string{},
		})
	}
	return out
}

func TestSelectFromPool_HonoursThePoolsOwnCount(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	stories := poolTestStories(now, 4)
	cfg := poolTestCfg()
	cfg.Count = 1 // must NOT be what the pool selects by

	tests := []struct {
		name  string
		count int
		want  int
	}{
		{"one", 1, 1},
		{"two", 2, 2},
		{"three", 3, 3},
		{"more than the pool holds", 9, 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectFromPool(poolPick{
				Name: "following", Label: "Following", Stories: stories, Count: tc.count,
			}, map[string]struct{}{}, cfg, false, now, rand.New(rand.NewSource(1)))
			if err != nil {
				t.Fatalf("selectFromPool: %v", err)
			}
			if len(got) != tc.want {
				t.Errorf("selected %d stories, want %d", len(got), tc.want)
			}
		})
	}
}

func TestSelectFromPool_BypassFlagDecidesTheAllSeenCase(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	stories := poolTestStories(now, 3)
	seen := map[string]struct{}{}
	for _, s := range stories {
		seen[s.Hash()] = struct{}{}
	}
	cfg := poolTestCfg()

	tests := []struct {
		name   string
		bypass bool
		want   int
	}{
		{"bypass off yields nothing", false, 0},
		{"bypass on re-shows a seen story", true, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectFromPool(poolPick{
				Name: "news", Label: "News", Stories: stories, Count: 1,
			}, seen, cfg, tc.bypass, now, rand.New(rand.NewSource(1)))
			if err != nil {
				t.Fatalf("selectFromPool: %v", err)
			}
			if len(got) != tc.want {
				t.Errorf("selected %d stories, want %d", len(got), tc.want)
			}
		})
	}
}

func TestSelectFromPool_EmptyPoolIsNotAnError(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	got, err := selectFromPool(poolPick{Name: "following", Label: "Following", Count: 1},
		map[string]struct{}{}, poolTestCfg(), true, now, rand.New(rand.NewSource(1)))
	if err != nil {
		t.Fatalf("a cold pool must not be an error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("selected %d stories from an empty pool, want 0", len(got))
	}
}

// seedCadenceState writes a feeds.json whose two feeds have known cadence
// rates: a publishes 8 dated items inside the 4-week window (2/week), b
// publishes 2 (0.5/week). The corpus median of {2, 0.5} is 1.25, both
// feeds are past the 4-week confidence ramp, so feedstate.Weights returns
// a → 1.25/2 = 0.625 and b → 1.25/0.5 = 2.5. Those are exact in binary
// floating point, which is what makes the overlay assertions below sharp.
func seedCadenceState(t *testing.T, now time.Time) {
	t.Helper()
	path, err := feedstate.Path()
	if err != nil {
		t.Fatalf("feedstate.Path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dates := func(n int) []string {
		out := make([]string, 0, n)
		for i := range n {
			out = append(out, now.Add(-time.Duration(i+1)*24*time.Hour).Format(time.RFC3339Nano))
		}
		return out
	}
	type feedJSON struct {
		URL          string   `json:"url"`
		FirstSeen    string   `json:"first_seen"`
		ObservedAt   string   `json:"observed_at"`
		PubDates     []string `json:"pub_dates"`
		EverDated    bool     `json:"ever_dated"`
		LastDocItems int      `json:"last_doc_items"`
		LastDocDated int      `json:"last_doc_dated"`
	}
	old := now.Add(-8 * 7 * 24 * time.Hour).Format(time.RFC3339Nano)
	file := struct {
		Version int        `json:"version"`
		Feeds   []feedJSON `json:"feeds"`
	}{
		Version: feedstate.SchemaVersion,
		Feeds: []feedJSON{
			{URL: "https://a.example/feed.xml", FirstSeen: old, ObservedAt: now.Format(time.RFC3339Nano),
				PubDates: dates(8), EverDated: true, LastDocItems: 8, LastDocDated: 8},
			{URL: "https://b.example/feed.xml", FirstSeen: old, ObservedAt: now.Format(time.RFC3339Nano),
				PubDates: dates(2), EverDated: true, LastDocItems: 2, LastDocDated: 2},
		},
	}
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatalf("marshal feedstate: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write feedstate: %v", err)
	}
}

func TestFeedWeights_NilWhenFollowingIsNotActive(t *testing.T) {
	isolateXDG(t)
	now := time.Now().UTC()
	seedCadenceState(t, now)

	tests := []struct {
		name string
		cfg  config.Config
	}{
		{"following not enabled", func() config.Config {
			c := poolTestCfg()
			c.Pools = []string{"news"}
			return c
		}()},
		{"following enabled with no feeds", func() config.Config {
			c := poolTestCfg()
			c.Following = config.FollowingConfig{}
			return c
		}()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := feedWeights(tc.cfg, now); got != nil {
				t.Errorf("feedWeights = %v, want nil (R-25: a user with no feeds pays nothing)", got)
			}
		})
	}
}

func TestFeedWeights_ManualWeightReplacesTheCadenceWeight(t *testing.T) {
	isolateXDG(t)
	now := time.Now().UTC()
	seedCadenceState(t, now)

	cfg := poolTestCfg()
	// Manual override on b only. Its cadence weight is 2.5, so a
	// multiplying overlay would yield 1.25 and a replacing overlay 0.5.
	cfg.Following.Feeds[1].Weight = 0.5

	got := feedWeights(cfg, now)
	want := map[string]float64{
		"https://a.example/feed.xml": 0.625,
		"https://b.example/feed.xml": 0.5,
	}
	if len(got) != len(want) {
		t.Fatalf("feedWeights = %v, want %v", got, want)
	}
	for url, w := range want {
		if math.Abs(got[url]-w) > 1e-9 {
			t.Errorf("weight[%s] = %v, want %v", url, got[url], w)
		}
	}
}

func TestFeedWeights_CadenceOnlyWhenNoManualOverride(t *testing.T) {
	isolateXDG(t)
	now := time.Now().UTC()
	seedCadenceState(t, now)

	got := feedWeights(poolTestCfg(), now)
	want := map[string]float64{
		"https://a.example/feed.xml": 0.625,
		"https://b.example/feed.xml": 2.5,
	}
	for url, w := range want {
		if math.Abs(got[url]-w) > 1e-9 {
			t.Errorf("weight[%s] = %v, want %v", url, got[url], w)
		}
	}
}

// TestFeedWeights_ManualWeightLandsWithNoCadenceEntry covers the feed
// that is absent from the cadence map entirely — here because feeds.json
// is unreadable, which is the honest way to produce an empty map. An
// overlay written as a multiplication into a missing key would produce 0
// and silently exclude the feed from selection.
func TestFeedWeights_ManualWeightLandsWithNoCadenceEntry(t *testing.T) {
	isolateXDG(t)
	now := time.Now().UTC()
	path, err := feedstate.Path()
	if err != nil {
		t.Fatalf("feedstate.Path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write feedstate: %v", err)
	}

	cfg := poolTestCfg()
	cfg.Following.Feeds[0].Weight = 3.0

	got := feedWeights(cfg, now)
	if math.Abs(got["https://a.example/feed.xml"]-3.0) > 1e-9 {
		t.Errorf("manual weight = %v, want 3.0 even with no cadence state", got["https://a.example/feed.xml"])
	}
}

func TestFallbackMessage(t *testing.T) {
	oneAggregatorFollowingActive := poolTestCfg()

	newsOnlySingle := poolTestCfg()
	newsOnlySingle.Pools = []string{"news"}

	newsOnlyMulti := newsOnlySingle
	newsOnlyMulti.News = config.NewsConfig{Aggregators: []string{"hackernews", "lobsters"}}

	noAggregators := newsOnlySingle
	noAggregators.News = config.NewsConfig{}

	tests := []struct {
		name         string
		cfg          config.Config
		wantContains string
		wantExact    string
	}{
		{
			name:         "one aggregator and no following pool names the provider",
			cfg:          newsOnlySingle,
			wantContains: "hackernews",
		},
		{
			name:      "one aggregator but following is active stays generic",
			cfg:       oneAggregatorFollowingActive,
			wantExact: defaults.FallbackMessage,
		},
		{
			name:      "two aggregators stay generic",
			cfg:       newsOnlyMulti,
			wantExact: defaults.FallbackMessage,
		},
		{
			name:      "no aggregators stay generic",
			cfg:       noAggregators,
			wantExact: defaults.FallbackMessage,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := fallbackMessage(tc.cfg)
			if tc.wantExact != "" && got != tc.wantExact {
				t.Errorf("fallbackMessage = %q, want %q", got, tc.wantExact)
			}
			if tc.wantContains != "" {
				if !strings.Contains(got, tc.wantContains) {
					t.Errorf("fallbackMessage = %q, want it to name %q", got, tc.wantContains)
				}
				if !strings.Contains(got, "check your connection") {
					t.Errorf("fallbackMessage = %q, want the connection hint kept", got)
				}
			}
		})
	}
}

// seedPoolCache writes one pool's cache file with the given stories and
// fetch time. fetchedAt is explicit because half these tests turn on
// whether a cache reads as fresh or stale.
func seedPoolCache(t *testing.T, pool string, fetchedAt time.Time, stories []fetch.Story) {
	t.Helper()
	path, err := cache.PoolPath(pool)
	if err != nil {
		t.Fatalf("cache.PoolPath(%q): %v", pool, err)
	}
	if err := cache.Write(path, &cache.File{
		Version:         cache.SchemaVersion,
		CachedByVersion: defaults.Version,
		FetchedAt:       fetchedAt,
		Stories:         stories,
	}); err != nil {
		t.Fatalf("seed %s cache: %v", pool, err)
	}
}

// captureSpawn swaps the detached-refresh seam for a counter and restores
// it. Every test that touches assemblePools must call it: a stale cache
// would otherwise fork a real background process out of the test suite.
func captureSpawn(t *testing.T) *int {
	t.Helper()
	var n int
	original := spawnRefresh
	spawnRefresh = func() { n++ }
	t.Cleanup(func() { spawnRefresh = original })
	return &n
}

// followingStory builds a feed-attributed story: Points 0 and Feed set,
// which is what makes rank.Score use its unit popularity numerator.
func followingStory(id, title, host, feed string, createdAt time.Time) fetch.Story {
	return fetch.Story{
		ID:        id,
		Title:     title,
		URL:       "https://" + host + "/" + id,
		Source:    "following",
		Feed:      feed,
		CreatedAt: createdAt,
		Tags:      []string{},
	}
}

// swapTestUnweightedFeed and swapTestWeightedFeed back
// TestAssemblePools_CadenceWeightsReachSelection and
// TestAssemblePools_AggregatorPoolIgnoresFollowingWeights, which share one
// numeric construction: a story at age 1h (storyU, feed
// swapTestUnweightedFeed) and one at age 2h (storyW, feed
// swapTestWeightedFeed). Raw age-based score alone favours storyU by
// roughly 1.68x; a 5x manual weight on swapTestWeightedFeed (only reachable
// through poolPick.Weights) reverses that to roughly 4.85x in storyW's
// favour. Hero selection is weighted-stochastic (rank.pickWeightedIndex), so
// rand.NewSource(1) is pinned deliberately: both outcomes below were checked
// against that seed's first draw, which is what makes "which story wins"
// deterministic rather than merely likely.
const (
	swapTestUnweightedFeed = "https://a.example/feed.xml" // poolTestCfg's feed 0; carries no weight entry
	swapTestWeightedFeed   = "https://b.example/feed.xml" // poolTestCfg's feed 1; gets the 5x manual override
)

// TestAssemblePools_CadenceWeightsReachSelection pins the wiring at
// pools.go:208 (assemblePools -> feedWeights -> poolPick.Weights): the
// per-feed weight map computed for the following pool must actually reach
// rank.SelectN, not merely exist. The branch's cadence machinery — several
// hundred lines of computation, plus feedstate's hot-path read — can be
// disconnected from selection entirely (p.Weights left nil, or feedWeights
// never called) and every OTHER following-pool test in this file still
// passes, because none of them picks a winner that depends on weighting.
// This one does: storyU is younger and wins on raw age-based score alone,
// but storyW's feed carries a 5x manual override that only a wired weight
// map can apply — and that override is what makes storyW win instead. If
// the wiring is severed, storyU wins here too and the test fails.
func TestAssemblePools_CadenceWeightsReachSelection(t *testing.T) {
	isolateXDG(t)
	captureSpawn(t)
	now := time.Now().UTC()

	storyU := followingStory("u", "Unweighted but newer", "host-u.example", swapTestUnweightedFeed, now.Add(-1*time.Hour))
	storyW := followingStory("w", "Weighted underdog", "host-w.example", swapTestWeightedFeed, now.Add(-2*time.Hour))

	cfg := poolTestCfg()
	cfg.Pools = []string{"following"}
	cfg.PoolOrder = []string{"following"}
	cfg.FollowingCount = 1
	cfg.Following.Feeds[1].Weight = 5.0 // feed b (swapTestWeightedFeed) gets the override
	seedPoolCache(t, "following", now.Add(-time.Minute), []fetch.Story{storyU, storyW})

	pools, _, err := assemblePools(cfg, map[string]struct{}{}, now, rand.New(rand.NewSource(1)), io.Discard)
	if err != nil {
		t.Fatalf("assemblePools: %v", err)
	}
	if len(pools) != 1 || len(pools[0].Stories) != 1 {
		t.Fatalf("pools = %+v, want exactly one following story", pools)
	}
	if got := pools[0].Stories[0].ID; got != "w" {
		t.Errorf("selected %q, want %q: the manual weight on its feed did not reach selection "+
			"(without it, the younger, unweighted story wins on raw score alone)", got, "w")
	}
}

// TestAssemblePools_AggregatorPoolIgnoresFollowingWeights pins the
// invariant poolPick documents at pools.go:30 — "Weights is nil for
// aggregator pools" — pools must never rank against each other. Nothing
// currently fails if that is violated. It reuses the exact scenario
// TestAssemblePools_CadenceWeightsReachSelection pins for the following
// pool, but seeds the NEWS (aggregator) pool with the same two stories
// instead. storyW's Feed happens to collide with a key in the following
// pool's weight map — Story.Feed is a field an aggregator story can
// structurally carry; nothing on fetch.Story restricts it to the following
// pool — so if a future change ever wired feedWeights into the news
// poolPick, storyW would win here exactly as it does in the following-pool
// test above. Under the correct code the news poolPick never receives a
// Weights map, so raw age-based score alone decides and the younger storyU
// wins.
func TestAssemblePools_AggregatorPoolIgnoresFollowingWeights(t *testing.T) {
	isolateXDG(t)
	captureSpawn(t)
	now := time.Now().UTC()

	storyU := fetch.Story{
		ID: "u", Title: "Unweighted but newer", URL: "https://host-u.example/x",
		Source: "hackernews", Feed: swapTestUnweightedFeed,
		CreatedAt: now.Add(-1 * time.Hour), Tags: []string{},
	}
	storyW := fetch.Story{
		ID: "w", Title: "Weighted underdog", URL: "https://host-w.example/x",
		Source: "hackernews", Feed: swapTestWeightedFeed,
		CreatedAt: now.Add(-2 * time.Hour), Tags: []string{},
	}

	cfg := poolTestCfg()
	// "following" stays active (in Pools, with its one feed) so
	// feedWeights(cfg, now) computes the same real 5x-override map as the
	// following-pool test — even though PoolOrder never processes the
	// following pool below, which is what isolates this assertion to
	// whether the map leaks into news, not whether it exists.
	cfg.Pools = []string{"following", "news"}
	cfg.PoolOrder = []string{"news"}
	cfg.Following.Feeds[1].Weight = 5.0
	seedPoolCache(t, "news", now.Add(-time.Minute), []fetch.Story{storyU, storyW})

	pools, _, err := assemblePools(cfg, map[string]struct{}{}, now, rand.New(rand.NewSource(1)), io.Discard)
	if err != nil {
		t.Fatalf("assemblePools: %v", err)
	}
	if len(pools) != 1 || len(pools[0].Stories) != 1 {
		t.Fatalf("pools = %+v, want exactly one news story", pools)
	}
	if got := pools[0].Stories[0].ID; got != "u" {
		t.Errorf("selected %q, want %q: the following pool's weight map reached the news pool's "+
			"selection (a following-feed-keyed weight reordered an aggregator pool)", got, "u")
	}
}

func TestAssemblePools_UsesEachPoolsOwnCount(t *testing.T) {
	isolateXDG(t)
	spawns := captureSpawn(t)
	now := time.Now().UTC()

	cfg := poolTestCfg()
	cfg.Count = 3
	cfg.FollowingCount = 2
	seedPoolCache(t, "news", now.Add(-time.Minute), poolTestStories(now, 4))
	seedPoolCache(t, "following", now.Add(-time.Minute), []fetch.Story{
		followingStory("f1", "Feed one", "a.example", "https://a.example/feed.xml", now.Add(-4*time.Hour)),
		followingStory("f2", "Feed two", "b.example", "https://b.example/feed.xml", now.Add(-6*time.Hour)),
		followingStory("f3", "Feed three", "c.example", "https://a.example/feed.xml", now.Add(-8*time.Hour)),
	})

	pools, rendered, err := assemblePools(cfg, map[string]struct{}{}, now, rand.New(rand.NewSource(1)), io.Discard)
	if err != nil {
		t.Fatalf("assemblePools: %v", err)
	}
	if len(pools) != 2 {
		t.Fatalf("got %d pools, want 2", len(pools))
	}
	if pools[0].Name != "following" || pools[1].Name != "news" {
		t.Fatalf("pools out of pool_order: %q, %q", pools[0].Name, pools[1].Name)
	}
	if len(pools[0].Stories) != 2 {
		t.Errorf("following pool selected %d stories, want 2 (following_count)", len(pools[0].Stories))
	}
	if len(pools[1].Stories) != 3 {
		t.Errorf("news pool selected %d stories, want 3 (count)", len(pools[1].Stories))
	}
	if len(rendered) != 5 {
		t.Errorf("rendered concatenation has %d stories, want 5", len(rendered))
	}
	if *spawns != 0 {
		t.Errorf("spawned %d refreshes from two fresh caches, want 0", *spawns)
	}
}

func TestAssemblePools_RenderedOrderMatchesPoolOrder(t *testing.T) {
	isolateXDG(t)
	captureSpawn(t)
	now := time.Now().UTC()

	cfg := poolTestCfg()
	seedPoolCache(t, "news", now.Add(-time.Minute), poolTestStories(now, 1))
	seedPoolCache(t, "following", now.Add(-time.Minute), []fetch.Story{
		followingStory("f1", "Feed one", "a.example", "https://a.example/feed.xml", now.Add(-4*time.Hour)),
	})

	pools, rendered, err := assemblePools(cfg, map[string]struct{}{}, now, rand.New(rand.NewSource(1)), io.Discard)
	if err != nil {
		t.Fatalf("assemblePools: %v", err)
	}
	if len(rendered) != 2 {
		t.Fatalf("got %d rendered stories, want 2", len(rendered))
	}
	if rendered[0].Title != "Feed one" {
		t.Errorf("rendered[0] = %q, want the following pool's story first", rendered[0].Title)
	}
	if rendered[1].Title != pools[1].Stories[0].Title {
		t.Errorf("rendered[1] = %q, want the news pool's story %q", rendered[1].Title, pools[1].Stories[0].Title)
	}
}

// TestAssemblePools_TwoPassBypass is ruling R-31's table: every pool is
// selected with the bypass OFF first; only if that leaves EVERY pool
// empty, while at least one pool had cached stories, does a second pass
// run in pool_order with the bypass ON — and the first pool to yield
// content wins, the rest staying empty.
func TestAssemblePools_TwoPassBypass(t *testing.T) {
	now := time.Now().UTC()
	newsStories := poolTestStories(now, 2)
	followingStories := []fetch.Story{
		followingStory("f1", "Feed one", "a.example", "https://a.example/feed.xml", now.Add(-4*time.Hour)),
		followingStory("f2", "Feed two", "b.example", "https://b.example/feed.xml", now.Add(-6*time.Hour)),
	}
	seenOf := func(stories ...[]fetch.Story) map[string]struct{} {
		out := map[string]struct{}{}
		for _, group := range stories {
			for _, s := range group {
				out[s.Hash()] = struct{}{}
			}
		}
		return out
	}

	tests := []struct {
		name          string
		pools         []string
		seen          map[string]struct{}
		wantFollowing int
		wantNews      int
	}{
		{
			name:          "all seen in the only pool still renders",
			pools:         []string{"news"},
			seen:          seenOf(newsStories),
			wantFollowing: 0,
			wantNews:      1,
		},
		{
			name:          "all-seen following with usable news renders only news",
			pools:         []string{"news", "following"},
			seen:          seenOf(followingStories),
			wantFollowing: 0,
			wantNews:      1,
		},
		{
			name:          "all seen everywhere renders the first pool in pool_order",
			pools:         []string{"news", "following"},
			seen:          seenOf(newsStories, followingStories),
			wantFollowing: 1,
			wantNews:      0,
		},
		{
			name:          "nothing seen renders both",
			pools:         []string{"news", "following"},
			seen:          map[string]struct{}{},
			wantFollowing: 1,
			wantNews:      1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateXDG(t)
			captureSpawn(t)
			cfg := poolTestCfg()
			cfg.Pools = tc.pools
			seedPoolCache(t, "news", now.Add(-time.Minute), newsStories)
			seedPoolCache(t, "following", now.Add(-time.Minute), followingStories)

			pools, _, err := assemblePools(cfg, tc.seen, now, rand.New(rand.NewSource(1)), io.Discard)
			if err != nil {
				t.Fatalf("assemblePools: %v", err)
			}
			got := map[string]int{}
			for _, p := range pools {
				got[p.Name] = len(p.Stories)
			}
			if got["following"] != tc.wantFollowing {
				t.Errorf("following pool rendered %d stories, want %d", got["following"], tc.wantFollowing)
			}
			if got["news"] != tc.wantNews {
				t.Errorf("news pool rendered %d stories, want %d", got["news"], tc.wantNews)
			}
		})
	}
}

// TestAssemblePools_ColdFollowingDoesNotBlock pins R-24: a missing
// following.json renders nothing for that pool and leaves the work to the
// detached refresh. It must not fan out to the feeds under the render
// timeout.
func TestAssemblePools_ColdFollowingDoesNotBlock(t *testing.T) {
	isolateXDG(t)
	spawns := captureSpawn(t)
	now := time.Now().UTC()

	cfg := poolTestCfg()
	seedPoolCache(t, "news", now.Add(-time.Minute), poolTestStories(now, 2))
	// No following.json at all.

	pools, rendered, err := assemblePools(cfg, map[string]struct{}{}, now, rand.New(rand.NewSource(1)), io.Discard)
	if err != nil {
		t.Fatalf("assemblePools: %v", err)
	}
	if len(pools) != 2 || len(pools[0].Stories) != 0 {
		t.Errorf("cold following pool should render nothing; got %d stories", len(pools[0].Stories))
	}
	if len(rendered) != 1 {
		t.Errorf("rendered %d stories, want 1 (news only)", len(rendered))
	}
	if *spawns != 1 {
		t.Errorf("spawned %d refreshes, want exactly 1 (following cache missing)", *spawns)
	}
}

func TestAssemblePools_SpawnsExactlyOneRefreshWhenBothCachesAreStale(t *testing.T) {
	isolateXDG(t)
	spawns := captureSpawn(t)
	now := time.Now().UTC()

	cfg := poolTestCfg()
	stale := now.Add(-2 * time.Hour) // TTL is 30m
	seedPoolCache(t, "news", stale, poolTestStories(now, 2))
	seedPoolCache(t, "following", stale, []fetch.Story{
		followingStory("f1", "Feed one", "a.example", "https://a.example/feed.xml", now.Add(-4*time.Hour)),
	})

	if _, _, err := assemblePools(cfg, map[string]struct{}{}, now, rand.New(rand.NewSource(1)), io.Discard); err != nil {
		t.Fatalf("assemblePools: %v", err)
	}
	if *spawns != 1 {
		t.Errorf("spawned %d refreshes for two stale pools, want exactly 1", *spawns)
	}
}

// TestAssemblePools_RefreshTurnsOnFreshnessNotEmptiness pins R-36. A pool
// that legitimately refreshed to zero stories is present and fresh, so it
// asks for nothing; collapsing "read failed" and "read fine, held nothing"
// into one stale flag made it spawn a detached refresh on every single
// terminal open, forever. The render rule is untouched either way: a
// present, fresh, empty pool still shows nothing.
func TestAssemblePools_RefreshTurnsOnFreshnessNotEmptiness(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name       string
		fetchedAt  time.Time
		wantSpawns int
	}{
		{"fresh and empty asks for nothing", now.Add(-time.Minute), 0},
		{"stale and empty still asks", now.Add(-2 * time.Hour), 1}, // TTL is 30m
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateXDG(t)
			spawns := captureSpawn(t)
			cfg := poolTestCfg()
			seedPoolCache(t, "news", now.Add(-time.Minute), poolTestStories(now, 2))
			seedPoolCache(t, "following", tc.fetchedAt, []fetch.Story{})

			pools, rendered, err := assemblePools(cfg, map[string]struct{}{}, now, rand.New(rand.NewSource(1)), io.Discard)
			if err != nil {
				t.Fatalf("assemblePools: %v", err)
			}
			if len(pools) != 2 || len(pools[0].Stories) != 0 {
				t.Errorf("a present-but-empty following pool must still render nothing; got %d pools, %d following stories", len(pools), len(pools[0].Stories))
			}
			if len(rendered) != 1 {
				t.Errorf("rendered %d stories, want 1 (news only)", len(rendered))
			}
			if *spawns != tc.wantSpawns {
				t.Errorf("spawned %d refreshes, want %d", *spawns, tc.wantSpawns)
			}
		})
	}
}

// TestAssemblePools_InactivePoolsAreSkippedEntirely pins R-35. Activity, not
// enablement, decides which pools are read: a user who empties [news]
// aggregators still has a feed.json full of stories on disk, and runRefresh
// already skips that pool — so reading it here would serve ghost stories
// while requesting a refresh that deliberately does nothing.
func TestAssemblePools_InactivePoolsAreSkippedEntirely(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name      string
		mutate    func(*config.Config)
		wantPools string // pool names in render order
	}{
		{"news enabled with no aggregators", func(c *config.Config) { c.News = config.NewsConfig{} }, "following"},
		{"following enabled with no feeds", func(c *config.Config) { c.Following = config.FollowingConfig{} }, "news"},
		{"news not in pools at all", func(c *config.Config) { c.Pools = []string{"following"} }, "following"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateXDG(t)
			spawns := captureSpawn(t)
			cfg := poolTestCfg()
			tc.mutate(&cfg)
			// BOTH caches exist and are stale. The inactive pool must
			// neither contribute stories nor ask for a refresh.
			stale := now.Add(-2 * time.Hour)
			seedPoolCache(t, "news", stale, poolTestStories(now, 2))
			seedPoolCache(t, "following", stale, []fetch.Story{
				followingStory("f1", "Feed one", "a.example", "https://a.example/feed.xml", now.Add(-4*time.Hour)),
			})

			pools, _, err := assemblePools(cfg, map[string]struct{}{}, now, rand.New(rand.NewSource(1)), io.Discard)
			if err != nil {
				t.Fatalf("assemblePools: %v", err)
			}
			names := make([]string, 0, len(pools))
			for _, pool := range pools {
				names = append(names, pool.Name)
			}
			if got := strings.Join(names, ","); got != tc.wantPools {
				t.Fatalf("pools = %q, want %q (the inactive pool must not be read)", got, tc.wantPools)
			}
			if *spawns != 1 {
				t.Errorf("spawned %d refreshes, want 1 (the one active pool is stale)", *spawns)
			}
		})
	}
}

// TestAssemblePools_CrossPoolDuplicateRendersOnce pins R-38.
// fetch.Story.Hash normalises URLs precisely so the same article on Hacker
// News and in a followed feed is ONE story; without a working seen set that
// grows as each pool picks, it renders in BOTH boxes in a single
// invocation. pool_order decides the winner, so following keeps it and news
// falls through to its second-choice story.
func TestAssemblePools_CrossPoolDuplicateRendersOnce(t *testing.T) {
	isolateXDG(t)
	captureSpawn(t)
	now := time.Now().UTC()

	const dupURL = "https://dup.example/the-one-article"
	newsDup := fetch.Story{
		ID: "hn-dup", Title: "The duplicated article", URL: dupURL,
		Source: "hackernews", Points: 500, CreatedAt: now.Add(-3 * time.Hour), Tags: []string{},
	}
	newsOther := fetch.Story{
		ID: "hn-2", Title: "Only on Hacker News", URL: "https://elsewhere.example/x",
		Source: "hackernews", Points: 400, CreatedAt: now.Add(-4 * time.Hour), Tags: []string{},
	}
	followingDup := fetch.Story{
		ID: "f-dup", Title: "The duplicated article", URL: dupURL,
		Source: "following", Feed: "https://a.example/feed.xml",
		CreatedAt: now.Add(-3 * time.Hour), Tags: []string{},
	}
	if newsDup.Hash() != followingDup.Hash() {
		t.Fatalf("test premise broken: %q != %q", newsDup.Hash(), followingDup.Hash())
	}

	cfg := poolTestCfg() // pool_order is following, then news
	// The news pool is allowed BOTH its stories. That is what makes this
	// test seed-independent: with the working set accumulating, the
	// contested article is already taken by the time news selects, so news
	// has exactly one candidate and can only ever return one story. Without
	// it, news selects from two candidates and must return two, duplicate
	// included. At count 1 the outcome instead rode on a weighted-random
	// choice between the 500-point duplicate and the 400-point alternative,
	// so a broken implementation escaped detection on roughly half the
	// seeds — including the one this test pins.
	cfg.Count = 2
	seedPoolCache(t, "news", now.Add(-time.Minute), []fetch.Story{newsDup, newsOther})
	seedPoolCache(t, "following", now.Add(-time.Minute), []fetch.Story{followingDup})

	pools, rendered, err := assemblePools(cfg, map[string]struct{}{}, now, rand.New(rand.NewSource(1)), io.Discard)
	if err != nil {
		t.Fatalf("assemblePools: %v", err)
	}
	counts := map[string]int{}
	for _, story := range rendered {
		counts[story.Hash()]++
	}
	if counts[newsDup.Hash()] != 1 {
		t.Errorf("the duplicated article rendered %d times, want exactly 1", counts[newsDup.Hash()])
	}
	if len(pools[0].Stories) != 1 || pools[0].Stories[0].Source != "following" {
		t.Errorf("following pool = %+v, want it to keep the contested article (earlier in pool_order)", pools[0].Stories)
	}
	if len(pools[1].Stories) != 1 || pools[1].Stories[0].Title != "Only on Hacker News" {
		t.Errorf("news pool = %+v, want exactly its one uncontested story even though count is 2", pools[1].Stories)
	}
}

// TestAssemblePools_CallersSeenMapIsNotMutated is the other half of R-38:
// the working set is a copy. The caller's map is history — recordHistory has
// not yet been told about this render — and must come back untouched.
func TestAssemblePools_CallersSeenMapIsNotMutated(t *testing.T) {
	isolateXDG(t)
	captureSpawn(t)
	now := time.Now().UTC()

	cfg := poolTestCfg()
	seedPoolCache(t, "news", now.Add(-time.Minute), poolTestStories(now, 2))
	seedPoolCache(t, "following", now.Add(-time.Minute), []fetch.Story{
		followingStory("f1", "Feed one", "a.example", "https://a.example/feed.xml", now.Add(-4*time.Hour)),
	})

	seen := map[string]struct{}{}
	_, rendered, err := assemblePools(cfg, seen, now, rand.New(rand.NewSource(1)), io.Discard)
	if err != nil {
		t.Fatalf("assemblePools: %v", err)
	}
	if len(rendered) == 0 {
		t.Fatal("test premise broken: nothing was selected, so nothing could have been added to seen")
	}
	if len(seen) != 0 {
		t.Errorf("caller's seen map grew to %d entries; assemblePools must copy it, not mutate it", len(seen))
	}
}

func TestWritePools_DispatchesByStyle(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	pools := []render.Pool{
		{Name: "following", Label: "Following", Stories: []fetch.Story{
			followingStory("f1", "Feed one", "a.example", "https://a.example/feed.xml", now.Add(-4*time.Hour)),
		}},
		{Name: "news", Label: "News", Stories: poolTestStories(now, 1)},
	}
	cfg := poolTestCfg()

	t.Run("minimal", func(t *testing.T) {
		cfgMin := cfg
		cfgMin.Style = "minimal"
		var buf bytes.Buffer
		if err := writePools(&buf, pools, cfgMin, now); err != nil {
			t.Fatalf("writePools: %v", err)
		}
		if got, want := buf.String(), render.MinimalPools(pools, now); got != want {
			t.Errorf("minimal dispatch\n got: %q\nwant: %q", got, want)
		}
		if strings.Contains(buf.String(), "╭") {
			t.Error("minimal style drew a box")
		}
	})

	t.Run("json", func(t *testing.T) {
		cfgJSON := cfg
		cfgJSON.Style = "json"
		var buf bytes.Buffer
		if err := writePools(&buf, pools, cfgJSON, now); err != nil {
			t.Fatalf("writePools: %v", err)
		}
		if got, want := buf.String(), render.JSONPools(pools, now); got != want {
			t.Errorf("json dispatch\n got: %q\nwant: %q", got, want)
		}
		var elements []map[string]any
		if err := json.Unmarshal(buf.Bytes(), &elements); err != nil {
			t.Fatalf("json style must emit an array (R-3): %v; got %q", err, buf.String())
		}
		if len(elements) != 2 || elements[0]["pool"] != "following" || elements[1]["pool"] != "news" {
			t.Errorf("pool stamps wrong: %v", elements)
		}
	})

	t.Run("boxed default", func(t *testing.T) {
		cfgBoxed := cfg
		cfgBoxed.Style = "boxed"
		var buf bytes.Buffer
		if err := writePools(&buf, pools, cfgBoxed, now); err != nil {
			t.Fatalf("writePools: %v", err)
		}
		want, err := render.Pools(pools, now, defaults.TermWidth(defaults.BoxWidth), render.MultiOptions{
			Marker: render.TickerMarker(cfgBoxed.TickerMarker),
			Boxed:  cfgBoxed.TickerBoxed,
		})
		if err != nil {
			t.Fatalf("render.Pools: %v", err)
		}
		if buf.String() != want {
			t.Errorf("boxed dispatch\n got: %q\nwant: %q", buf.String(), want)
		}
	})
}

// TestWritePools_NothingToShowIsStyleAware pins the fix for a render-path
// bug: the all-empty render used to bypass style dispatch entirely and
// print an English sentence, so `newsfetch --style=json | jq` failed on a
// healthy install the moment a pool read as present-but-empty. R-3's
// uniform-array contract has no exception for "nothing to show" — an
// empty array IS the answer, and it parses.
func TestWritePools_NothingToShowIsStyleAware(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	cfg := poolTestCfg()
	// Two pools that read fine and hold nothing — the ordinary state
	// after a following pool legitimately refreshes to zero stories.
	empty := []render.Pool{
		{Name: "following", Label: "Following"},
		{Name: "news", Label: "News"},
	}

	t.Run("json emits a parseable empty array", func(t *testing.T) {
		cfgJSON := cfg
		cfgJSON.Style = "json"
		var buf bytes.Buffer
		if err := writePools(&buf, empty, cfgJSON, now); err != nil {
			t.Fatalf("writePools: %v", err)
		}
		var elements []map[string]any
		if err := json.Unmarshal(buf.Bytes(), &elements); err != nil {
			t.Fatalf("json style must stay an array with nothing to show (R-3): %v; got %q", err, buf.String())
		}
		if len(elements) != 0 {
			t.Errorf("got %d elements, want 0: %q", len(elements), buf.String())
		}
	})

	for _, style := range []string{"boxed", "minimal"} {
		t.Run(style+" keeps the fallback message", func(t *testing.T) {
			cfgStyle := cfg
			cfgStyle.Style = style
			var buf bytes.Buffer
			if err := writePools(&buf, empty, cfgStyle, now); err != nil {
				t.Fatalf("writePools: %v", err)
			}
			if want := render.Fallback(fallbackMessage(cfgStyle)); buf.String() != want {
				t.Errorf("%s style with nothing to show\n got: %q\nwant: %q", style, buf.String(), want)
			}
		})
	}
}

// TestAssemblePools_SecondPassSkipsPastEmptyPoolsToTheFirstThatYields
// pins the fall-through half of R-31's second pass. The pass runs in
// pool_order and stops at the first pool that YIELDS, which is not the
// same as stopping at the first pool: a pool that read cleanly and held
// nothing is still in the list, still first in pool_order, and still
// cannot repeat anything. Stopping there would print the offline
// fallback on a working install with a perfectly usable news cache.
func TestAssemblePools_SecondPassSkipsPastEmptyPoolsToTheFirstThatYields(t *testing.T) {
	isolateXDG(t)
	captureSpawn(t)
	now := time.Now().UTC()

	cfg := poolTestCfg() // pool_order: following, then news
	newsStories := poolTestStories(now, 2)
	seen := map[string]struct{}{}
	for _, s := range newsStories {
		seen[s.Hash()] = struct{}{}
	}
	// Following refreshed to nothing; news holds only stories the user has
	// already been shown. Pass one therefore selects nothing anywhere, and
	// pass two has to walk past following to find the pool that can repeat.
	seedPoolCache(t, "following", now.Add(-time.Minute), []fetch.Story{})
	seedPoolCache(t, "news", now.Add(-time.Minute), newsStories)

	pools, rendered, err := assemblePools(cfg, seen, now, rand.New(rand.NewSource(1)), io.Discard)
	if err != nil {
		t.Fatalf("assemblePools: %v", err)
	}
	if len(rendered) != 1 {
		t.Fatalf("rendered %d stories, want 1 — a repeat from news beats the offline fallback", len(rendered))
	}
	got := map[string]int{}
	for _, p := range pools {
		got[p.Name] = len(p.Stories)
	}
	if got["following"] != 0 {
		t.Errorf("following pool rendered %d stories, want 0 (it had none to repeat)", got["following"])
	}
	if got["news"] != 1 {
		t.Errorf("news pool rendered %d stories, want 1 (the second pass must reach it)", got["news"])
	}
}

// TestAssemblePools_EmptyNewsCacheStillTriggersTheColdFetch pins the
// trigger on the news pool's synchronous cold-start fetch: it is
// emptiness, not absence. A feed.json that read cleanly and held zero
// stories has nothing to render, so keying the fetch on !present would
// leave that user staring at the fallback until the detached refresh
// happened to land. This is the pre-pool behaviour carried over
// deliberately, and it is the one place emptiness and presence part ways
// on purpose — R-36 governs the REFRESH decision, not this one.
func TestAssemblePools_EmptyNewsCacheStillTriggersTheColdFetch(t *testing.T) {
	isolateXDG(t)
	captureSpawn(t)
	ts := algoliaStub()
	defer ts.Close()
	swapHNSource(t, ts.URL)

	now := time.Now().UTC()
	cfg := poolTestCfg()
	cfg.Pools = []string{"news"} // following inactive: one pool under test
	// Present AND fresh AND empty — the state that separates the two rules.
	seedPoolCache(t, "news", now.Add(-time.Minute), []fetch.Story{})

	pools, rendered, err := assemblePools(cfg, map[string]struct{}{}, now, rand.New(rand.NewSource(1)), io.Discard)
	if err != nil {
		t.Fatalf("assemblePools: %v", err)
	}
	if len(pools) != 1 {
		t.Fatalf("got %d pools, want 1", len(pools))
	}
	if len(rendered) != 1 {
		t.Fatalf("rendered %d stories, want 1 from the cold fetch", len(rendered))
	}
	if len(pools[0].Stories) != 1 {
		t.Errorf("news pool rendered %d stories, want 1", len(pools[0].Stories))
	}
}

// TestAssemblePools_OfflineColdStartAsksForABackgroundRefresh pins a
// deliberate behaviour change against the pre-pool render path. There, a
// cold cache plus a failed synchronous fetch printed the fallback and
// spawned nothing. Here the missing cache marks the pool as needing a
// refresh before the cold fetch is attempted, so a detached refresh goes
// out even though this invocation renders nothing. That is the better
// behaviour and it is intended: a briefly flaky network costs the user one
// empty render instead of an empty render on every terminal open until a
// stale-cache path happens to fire.
func TestAssemblePools_OfflineColdStartAsksForABackgroundRefresh(t *testing.T) {
	isolateXDG(t)
	spawns := captureSpawn(t)
	// A reachable upstream that has nothing to give: the fetch succeeds and
	// yields zero stories, which is the same dead end as a failure without
	// needing an error to be plumbed through the stub.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"hits":[]}`)
	}))
	defer ts.Close()
	swapHNSource(t, ts.URL)

	now := time.Now().UTC()
	cfg := poolTestCfg()
	cfg.Pools = []string{"news"}
	// No cache file at all.

	pools, rendered, err := assemblePools(cfg, map[string]struct{}{}, now, rand.New(rand.NewSource(1)), io.Discard)
	if err != nil {
		t.Fatalf("assemblePools: %v", err)
	}
	if len(pools) != 1 || len(pools[0].Stories) != 0 {
		t.Errorf("news pool = %+v, want nothing to render", pools)
	}
	if len(rendered) != 0 {
		t.Errorf("rendered %d stories, want 0", len(rendered))
	}
	if *spawns != 1 {
		t.Errorf("spawned %d refreshes, want exactly 1 — an empty render must still leave the next one warm", *spawns)
	}
}

// removedFeedURL is a feed URL that appears in no test config: seeding a
// following cache with a story attributed to it models the user having
// unsubscribed from that feed while its stories were still cached.
const removedFeedURL = "https://removed.example/feed.xml"

// TestAssemblePools_RemovedFeedIsNotRendered pins the render-side feed
// filter. Dropping a story whose feed the user unsubscribed from happens in
// mergeFollowingStories, which runs only when a refresh got at least one
// feed result back — so an unsubscribe followed by a run of total refresh
// failures leaves the removed feed's stories in following.json for as long
// as the failures last. The render path must not serve them: the CONFIGURED
// feed list decides what the following pool may show, not what happens to be
// on disk.
//
// The seeded cache is fresh and holds nothing but the removed feed's story,
// so no other rule can carry the assertion: without the filter that story is
// the pool's only candidate and it renders.
func TestAssemblePools_RemovedFeedIsNotRendered(t *testing.T) {
	isolateXDG(t)
	captureSpawn(t)
	now := time.Now().UTC()

	cfg := poolTestCfg()
	cfg.Pools = []string{"following"}
	cfg.PoolOrder = []string{"following"}
	seedPoolCache(t, "following", now.Add(-time.Minute), []fetch.Story{
		followingStory("gone", "From a feed the user removed", "removed.example", removedFeedURL, now.Add(-2*time.Hour)),
	})

	pools, rendered, err := assemblePools(cfg, map[string]struct{}{}, now, rand.New(rand.NewSource(1)), io.Discard)
	if err != nil {
		t.Fatalf("assemblePools: %v", err)
	}
	if len(pools) != 1 || pools[0].Name != "following" {
		t.Fatalf("pools = %+v, want exactly the following pool", pools)
	}
	if len(pools[0].Stories) != 0 {
		t.Errorf("following pool rendered %d stories, want 0 — %q is no longer configured", len(pools[0].Stories), removedFeedURL)
	}
	if len(rendered) != 0 {
		t.Errorf("rendered %d stories, want 0", len(rendered))
	}
}
