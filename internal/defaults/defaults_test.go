package defaults

import (
	"slices"
	"testing"
)

func TestClampWidth(t *testing.T) {
	const fallback = 80
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"zero -> fallback", 0, fallback},
		{"negative -> fallback", -1, fallback},
		{"just below min -> fallback", 39, fallback},
		{"at min -> passes through", 40, 40},
		{"just above min -> passes through", 41, 41},
		{"mid-range -> passes through", 80, 80},
		{"just below max -> passes through", 99, 99},
		{"at max -> passes through", 100, 100},
		{"just above max -> clamps to 100", 101, 100},
		{"huge -> clamps to 100", 10_000, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clampWidth(tc.in, fallback)
			if got != tc.want {
				t.Errorf("clampWidth(%d, %d) = %d, want %d", tc.in, fallback, got, tc.want)
			}
		})
	}
}

// TestTermWidth_NonTTYFallsBack exercises the path where x/term.GetSize
// returns an error (because go test redirects stdout to a pipe). In that
// case TermWidth must return the caller's fallback value verbatim.
func TestTermWidth_NonTTYFallsBack(t *testing.T) {
	if got := TermWidth(BoxWidth); got != BoxWidth {
		t.Errorf("TermWidth under non-TTY = %d, want %d (fallback)", got, BoxWidth)
	}
	if got := TermWidth(73); got != 73 {
		t.Errorf("TermWidth under non-TTY should echo fallback; got %d want 73", got)
	}
}

// TestSources_ReturnsCopy pins the registry's immutability, matching
// fetch.KnownSourceNames and render.KnownTickerMarkers: a caller mutating
// the returned slice must not affect what later callers see.
func TestSources_ReturnsCopy(t *testing.T) {
	a := Sources()
	a[0] = "mutated"
	if b := Sources(); b[0] == "mutated" {
		t.Error("Sources shares its backing array; want a fresh copy per call")
	}
}

// TestFeedBounds pins the ordering the four feed constants only mean
// anything as a set: internal/feedstate caps a computed cadence weight at
// MaxFeedWeight, and internal/config clamps a configured max_items into
// [MinFeedItems, MaxFeedItems] and a configured weight into
// (0, MaxFeedWeight]. An inverted pair would turn every clamp into a
// rejection of every value, and nothing downstream re-checks the ordering.
func TestFeedBounds(t *testing.T) {
	if MinFeedItems < 1 {
		t.Errorf("MinFeedItems = %d, want at least 1 (zero silently disables a feed the user configured)", MinFeedItems)
	}
	if MinFeedItems > DefaultFeedMaxItems {
		t.Errorf("MinFeedItems = %d > DefaultFeedMaxItems = %d; the default must be a legal value", MinFeedItems, DefaultFeedMaxItems)
	}
	if DefaultFeedMaxItems > MaxFeedItems {
		t.Errorf("DefaultFeedMaxItems = %d > MaxFeedItems = %d; the default must be a legal value", DefaultFeedMaxItems, MaxFeedItems)
	}
	if MaxFeedWeight <= 0 {
		t.Errorf("MaxFeedWeight = %v, want a positive cap (weights multiply a score; a non-positive cap zeroes every feed)", MaxFeedWeight)
	}
}

// TestPoolRegistries_ReturnCopies pins the copy-returning registry
// convention for the pool lists, matching TestSources_ReturnsCopy above: a
// caller mutating the returned slice must not affect what later callers
// see. config.Validate assigns Pools() straight into a Config on its
// fallback path, so a shared backing array would let one bad config poison
// every later one in the same process.
func TestPoolRegistries_ReturnCopies(t *testing.T) {
	cases := []struct {
		name string
		fn   func() []string
	}{
		{"Pools", Pools},
		{"PoolOrder", PoolOrder},
		{"KnownPools", KnownPools},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := tc.fn()
			a[0] = "mutated"
			if b := tc.fn(); b[0] == "mutated" {
				t.Errorf("%s shares its backing array; want a fresh copy per call", tc.name)
			}
		})
	}
}

// TestPoolOrder_CoversEveryKnownPool pins the invariant config's pool_order
// normalisation leans on: it appends the pools a user left out of
// pool_order by walking PoolOrder(), so a pool that exists in KnownPools
// but is missing from PoolOrder would be enabled and never rendered.
func TestPoolOrder_CoversEveryKnownPool(t *testing.T) {
	order := PoolOrder()
	known := KnownPools()
	if len(order) != len(known) {
		t.Fatalf("PoolOrder() = %v (%d), KnownPools() = %v (%d); want the same names",
			order, len(order), known, len(known))
	}
	for _, k := range known {
		if !slices.Contains(order, k) {
			t.Errorf("known pool %q missing from PoolOrder() %v", k, order)
		}
	}
	for _, o := range order {
		if !slices.Contains(known, o) {
			t.Errorf("PoolOrder() names %q, which is not a known pool %v", o, known)
		}
	}
}

// TestPoolOrder_DefaultsToFollowingFirst pins the design's "curated wins the
// prime slot" rule. A user who enables the following pool by hand and never
// writes pool_order gets this order, so flipping it is a user-visible
// change, not a cosmetic one.
func TestPoolOrder_DefaultsToFollowingFirst(t *testing.T) {
	if got := PoolOrder(); got[0] != "following" {
		t.Errorf("PoolOrder()[0] = %q, want %q", got[0], "following")
	}
}

// TestPools_FollowingShipsDisabled pins that a first-run user gets the news
// pool only. Following with no configured feeds would render nothing, so
// enabling it by default would only cost startup work.
func TestPools_FollowingShipsDisabled(t *testing.T) {
	if got := Pools(); !slices.Equal(got, []string{"news"}) {
		t.Errorf("Pools() = %v, want [news]", got)
	}
}

func TestPoolLabel(t *testing.T) {
	cases := []struct {
		name string
		pool string
		want string
	}{
		{"news", "news", "News"},
		{"following", "following", "Following"},
		{"unknown pool has no label", "repos", ""},
		{"empty name has no label", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PoolLabel(tc.pool); got != tc.want {
				t.Errorf("PoolLabel(%q) = %q, want %q", tc.pool, got, tc.want)
			}
		})
	}
}

// TestFollowingTimeouts_ParentExceedsPerFeed pins the addendum item 7
// relationship: the fan-out parent must be larger than the per-feed
// sub-timeout, or the per-feed budget is clipped to nothing and one slow
// host fails every feed behind it.
func TestFollowingTimeouts_ParentExceedsPerFeed(t *testing.T) {
	if FollowingFetchTimeout <= FollowingPerFeedTimeout {
		t.Errorf("FollowingFetchTimeout = %v must exceed FollowingPerFeedTimeout = %v",
			FollowingFetchTimeout, FollowingPerFeedTimeout)
	}
	if FollowingPerFeedTimeout <= FetchTimeout {
		t.Errorf("FollowingPerFeedTimeout = %v should exceed the render-path FetchTimeout = %v; the fan-out is detached and can afford more",
			FollowingPerFeedTimeout, FetchTimeout)
	}
}
