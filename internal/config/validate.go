package config

import (
	"fmt"
	"io"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/defaults"
	"github.com/PietroCoppola/newsfetch/internal/fetch"
	"github.com/PietroCoppola/newsfetch/internal/render"
)

// FieldSources tells Validate where each validatable field came from so
// warnings can name the source. Zero value for a field means "from defaults"
// and produces no warning (defaults are valid by construction).
type FieldSources struct {
	// Style is "" (defaults), "config", or "flag".
	Style string
	// Count is "" (defaults), "config", or "flag".
	Count string
}

// minCacheTTL is the validation floor for cache_ttl_minutes. It lives here
// (not in internal/defaults) because it's a validation concern — the default
// TTL sits comfortably above this floor; the floor only matters when a user
// sets a too-small value via config or flag.
const minCacheTTL = 5 * time.Minute

// Validate inspects the merged Config, clamps out-of-range fields, and emits
// at most one warning line to w naming the offending field and its source.
// Returns the corrected Config. Callers pass os.Stderr in production and a
// bytes.Buffer in tests. Not called when Load returned a parse error —
// defaults are valid by construction.
//
// Precedence of warnings when multiple fields are bad (first wins):
//  1. unknown style
//  2. pools empty or all-unknown
//  3. unknown pool name(s) dropped
//  4. every enabled pool has an empty internal config
//  5. unknown [news] aggregators dropped
//  6. cache_ttl_minutes below minimum
//  7. min_points below 0
//  8. count out of [1, MaxCount]
//  9. following_count out of [1, MaxCount]
//  10. unknown ticker_marker
//  11. dedup_ttl_hours negative
//  12. per-feed problems, aggregated into one line
//
// pool_order has no slot in the cascade: it is normalised into a permutation
// of the enabled pools silently, because a user who enables a pool and
// forgets to name it in pool_order has not made a mistake worth a line of
// stderr on every terminal open.
//
// Feed problems sit last so a malformed [[following.feeds]] block cannot
// mask a style or TTL warning, which the user is far more likely to act on.
// They are checked whether or not the following pool is enabled: the warning
// is about the config file, which is what the user edits.
//
// Every early return must route through silentlyCorrect so fields below the
// first warning still get clamped. If a new field is added to the cascade,
// extend silentlyCorrect to cover it.
//
// Validate is pure and idempotent — the statusline path runs it a second
// time against io.Discard, and pool_order's permutation normalisation has to
// survive that without appending anything twice.
func Validate(c Config, src FieldSources, w io.Writer) Config {
	minMins := int(minCacheTTL / time.Minute)
	// These two corrections happen before the cascade because entries
	// further down read their results: the all-empty-pools rule must see
	// the aggregator list AFTER unknown names are dropped and the feed list
	// AFTER unusable feeds are dropped, or it would mistake garbage for
	// content and leave the user staring at an empty render.
	aggValid, aggDropped := splitSources(c.News.Aggregators)
	c.News.Aggregators = aggValid
	feeds, feedIss := splitFeeds(c.Following.Feeds)
	c.Following.Feeds = feeds

	switch c.Style {
	case "boxed", "minimal", "json":
	case "statusline":
		// Valid from the flag only: statusline is an invocation mode for
		// the Claude Code statusline script, not a daily-driver default.
		// Persisted in config it would make every terminal open render a
		// bare linked line — or nothing at all on a cold cache, since the
		// statusline path never blocks on the network. The settings
		// surfaces deliberately do not offer it.
		if src.Style != "flag" {
			bad := c.Style
			c.Style = Defaults().Style
			fmt.Fprintf(w, "newsfetch: style %q is flag-only (use --style=statusline), using %q\n", bad, c.Style)
			return silentlyCorrect(c)
		}
	default:
		bad := c.Style
		c.Style = Defaults().Style
		fmt.Fprintf(w, "newsfetch: unknown style %q (%s), using %q\n", bad, sourceLabel(src.Style, "style"), c.Style)
		return silentlyCorrect(c)
	}
	poolsValid, poolsDropped := splitPools(c.Pools)
	if len(poolsValid) == 0 {
		// Either the user wrote pools=[], or every name was unknown.
		// Either way nothing would ever render, so fail loud and reset.
		c.Pools = defaults.Pools()
		if len(poolsDropped) > 0 {
			fmt.Fprintf(w, "newsfetch: pools contained no recognised names (dropped: %v); using %v\n", poolsDropped, c.Pools)
		} else {
			fmt.Fprintf(w, "newsfetch: pools is empty; using %v\n", c.Pools)
		}
		return silentlyCorrect(c)
	}
	c.Pools = poolsValid
	if len(poolsDropped) > 0 {
		fmt.Fprintf(w, "newsfetch: unknown pool name(s) %v dropped; using %v\n", poolsDropped, c.Pools)
		return silentlyCorrect(c)
	}
	if !anyPoolHasContent(c) {
		// Restore BOTH halves. Clamping pools alone is a no-op when news
		// is already enabled with zero aggregators — a warning that
		// announces a fix which changed nothing is worse than no warning.
		c.Pools = defaults.Pools()
		c.News.Aggregators = defaults.Sources()
		fmt.Fprintf(w, "newsfetch: pools produced no content; using %v with aggregators %v\n", c.Pools, c.News.Aggregators)
		return silentlyCorrect(c)
	}
	if len(aggDropped) > 0 {
		// Sits below the all-empty rule on purpose: reaching here means
		// something still has content, so the list printed is the one the
		// user ends up running with rather than a value silentlyCorrect is
		// about to overwrite.
		fmt.Fprintf(w, "newsfetch: unknown aggregator name(s) %v dropped; using %v\n", aggDropped, c.News.Aggregators)
		return silentlyCorrect(c)
	}
	if c.CacheTTL < minCacheTTL {
		badMins := int(c.CacheTTL / time.Minute)
		c.CacheTTL = minCacheTTL
		fmt.Fprintf(w, "newsfetch: cache_ttl_minutes=%d below minimum %d, using %d\n", badMins, minMins, minMins)
		return silentlyCorrect(c)
	}
	if c.MinPoints < 0 {
		bad := c.MinPoints
		c.MinPoints = 0
		fmt.Fprintf(w, "newsfetch: min_points=%d below 0, using 0\n", bad)
		return silentlyCorrect(c)
	}
	if c.Count < 1 || c.Count > defaults.MaxCount {
		bad := c.Count
		c.Count = clampCount(c.Count)
		fmt.Fprintf(w, "newsfetch: count=%d out of [1, %d] (%s), using %d\n", bad, defaults.MaxCount, sourceLabel(src.Count, "count"), c.Count)
		return silentlyCorrect(c)
	}
	if c.FollowingCount < 1 || c.FollowingCount > defaults.MaxCount {
		// No source label: following_count has no flag, so config is the
		// only origin a user can have typed it from.
		bad := c.FollowingCount
		c.FollowingCount = clampCount(c.FollowingCount)
		fmt.Fprintf(w, "newsfetch: following_count=%d out of [1, %d] (from config), using %d\n", bad, defaults.MaxCount, c.FollowingCount)
		return silentlyCorrect(c)
	}
	if !knownTickerMarker(c.TickerMarker) {
		bad := c.TickerMarker
		c.TickerMarker = Defaults().TickerMarker
		fmt.Fprintf(w, "newsfetch: unknown ticker_marker %q (from config), using %q\n", bad, c.TickerMarker)
		return silentlyCorrect(c)
	}
	if c.DedupWindow < 0 {
		bad := int(c.DedupWindow / time.Hour)
		c.DedupWindow = 0
		fmt.Fprintf(w, "newsfetch: dedup_ttl_hours=%d negative, treating as 0 (history dedup disabled)\n", bad)
		return silentlyCorrect(c)
	}
	if feedIss.any() {
		fmt.Fprintf(w, "newsfetch: %s\n", feedIss.warning())
		return silentlyCorrect(c)
	}
	// The clean path routes through silentlyCorrect too, so pool_order
	// normalisation and the clamps have exactly one implementation rather
	// than one here and one there that can drift apart. Every clamp is a
	// no-op by the time we reach this line.
	return silentlyCorrect(c)
}

// clampCount snaps Count into [1, MaxCount]. Used by the validator's
// warning path and by silentlyCorrect.
func clampCount(n int) int {
	if n < 1 {
		return 1
	}
	if n > defaults.MaxCount {
		return defaults.MaxCount
	}
	return n
}

func knownTickerMarker(name string) bool {
	for _, m := range render.KnownTickerMarkers() {
		if string(m) == name {
			return true
		}
	}
	return false
}

// silentlyCorrect applies the remaining clamps without emitting further
// warnings. Used after the first warning fires so the rest of the config
// still ends up in a usable state.
func silentlyCorrect(c Config) Config {
	if c.CacheTTL < minCacheTTL {
		c.CacheTTL = minCacheTTL
	}
	if c.MinPoints < 0 {
		c.MinPoints = 0
	}
	if valid, _ := splitPools(c.Pools); len(valid) == 0 {
		c.Pools = defaults.Pools()
	} else {
		c.Pools = valid
	}
	c.News.Aggregators, _ = splitSources(c.News.Aggregators)
	c.Following.Feeds, _ = splitFeeds(c.Following.Feeds)
	if !anyPoolHasContent(c) {
		c.Pools = defaults.Pools()
		c.News.Aggregators = defaults.Sources()
	}
	// pool_order is settled last: it is a permutation of the ENABLED pools,
	// so it can only be computed once every rule that can change Pools has
	// run.
	c.PoolOrder = orderPools(c.PoolOrder, c.Pools)
	c.Count = clampCount(c.Count)
	c.FollowingCount = clampCount(c.FollowingCount)
	if !knownTickerMarker(c.TickerMarker) {
		c.TickerMarker = Defaults().TickerMarker
	}
	if c.DedupWindow < 0 {
		c.DedupWindow = 0
	}
	return c
}

// splitPools partitions pool names into recognised vs unknown, preserving
// order and collapsing duplicates. Recognition uses defaults.KnownPools and
// deliberately NOT fetch.KnownSourceNames: following is a pool, not an
// aggregator, and keeping the two registries apart is exactly what makes
// aggregators = ["following"] impossible to spell. A duplicate is dropped
// silently — it is not a mistake worth a warning, but left in place it would
// render the same pool's box twice.
func splitPools(names []string) (valid, dropped []string) {
	seen := make(map[string]struct{}, len(names))
	for _, n := range names {
		if !knownPool(n) {
			dropped = append(dropped, n)
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		valid = append(valid, n)
	}
	return valid, dropped
}

func knownPool(name string) bool {
	for _, k := range defaults.KnownPools() {
		if k == name {
			return true
		}
	}
	return false
}

// orderPools normalises a configured pool_order into a permutation of the
// enabled pools: names the user listed come first in their order, then any
// enabled pool they left out, in compile-time order. Disabled names,
// unknown names and duplicates are dropped.
//
// Walking defaults.PoolOrder() for the leftovers is safe because it is
// pinned to cover every name in defaults.KnownPools (see
// TestPoolOrder_CoversEveryKnownPool), and splitPools has already dropped
// anything outside that registry — so every enabled pool is guaranteed a
// slot and the render path can walk the result without re-checking
// enablement.
func orderPools(order, enabled []string) []string {
	isEnabled := func(name string) bool {
		for _, e := range enabled {
			if e == name {
				return true
			}
		}
		return false
	}
	placed := make(map[string]struct{}, len(enabled))
	var out []string
	add := func(name string) {
		if _, done := placed[name]; done || !isEnabled(name) {
			return
		}
		placed[name] = struct{}{}
		out = append(out, name)
	}
	for _, n := range order {
		add(n)
	}
	for _, n := range defaults.PoolOrder() {
		add(n)
	}
	return out
}

// anyPoolHasContent reports whether at least one enabled pool has something
// to fetch. A pool with an empty internal config renders nothing, so a
// config where that is true of every enabled pool renders nothing at all,
// forever, without ever saying why. Unknown names cannot reach here —
// splitPools drops them first — so the switch needs no default arm.
func anyPoolHasContent(c Config) bool {
	for _, p := range c.Pools {
		switch p {
		case "news":
			if len(c.News.Aggregators) > 0 {
				return true
			}
		case "following":
			if len(c.Following.Feeds) > 0 {
				return true
			}
		}
	}
	return false
}

// splitSources partitions aggregator names into recognised vs unknown,
// preserving order. Recognition uses fetch.KnownSourceNames as the single
// source of truth.
func splitSources(names []string) (valid, dropped []string) {
	for _, n := range names {
		if knownSource(n) {
			valid = append(valid, n)
		} else {
			dropped = append(dropped, n)
		}
	}
	return valid, dropped
}

func knownSource(name string) bool {
	for _, k := range fetch.KnownSourceNames() {
		if k == name {
			return true
		}
	}
	return false
}

// splitFeeds returns the usable feeds with their per-feed knobs clamped,
// plus everything that went wrong along the way. Step 6 of this task fills
// in the per-feed rules.
func splitFeeds(feeds []FeedConfig) ([]FeedConfig, feedIssues) {
	var out []FeedConfig
	var iss feedIssues
	out = append(out, feeds...)
	return out, iss
}

// feedIssues accumulates every per-feed problem in one config so Validate
// can spend its single warning line on a count instead of one line per feed.
type feedIssues struct {
	dropped       int
	dropReasons   []string
	adjusted      int
	adjustReasons []string
}

func (f feedIssues) any() bool { return f.dropped > 0 || f.adjusted > 0 }

// warning renders the single line Validate prints for every feed problem in
// the file at once, e.g. "2 feeds dropped: invalid url".
func (f feedIssues) warning() string { return "" }

// sourceLabel renders the human-readable origin tag in a warning. flagName
// is the long flag name without leading dashes (e.g. "style", "count");
// callers pass the flag they would have used to set the offending field.
func sourceLabel(src, flagName string) string {
	switch src {
	case "flag":
		return "from --" + flagName
	case "config":
		return "from config"
	default:
		return "from defaults"
	}
}
