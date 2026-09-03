package config

import (
	"fmt"
	"io"
	"math"
	"net/url"
	"strings"
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

// MinCacheTTLMinutes, MinPointsFloor, MinDedupTTLHours, MaxCacheTTLMinutes
// and MaxDedupTTLHours are the validation bounds for the advanced config
// knobs, spelled in the units the config file uses. They live here (not in
// internal/defaults) because they are a validation concern — every default
// sits comfortably inside them, and a bound only matters once a user types a
// value.
//
// Exported because the TOML path is not the only one that has to honour
// them: the scripted --init/--settings JSON readers reject what this package
// clamps, and they must reject it at exactly these numbers. A bound spelled
// twice is a bound that drifts, and the two halves disagreeing is how an
// invalid value gets written to a config file that Validate then only ever
// repairs in memory.
//
// MaxCacheTTLMinutes and MaxDedupTTLHours exist because Load multiplies the
// raw config int by time.Minute / time.Hour to build a time.Duration — a
// signed 64-bit count of nanoseconds. One minute or hour past either bound
// overflows that multiplication negative, and the floor above then clamps
// the result to the minimum: the user gets a value they never typed, from
// input this package's own floor check accepted. Derived from math.MaxInt64
// rather than pasted as a literal so the bound cannot drift from the
// language's own limit; min_points has no such bound because Load never
// multiplies it.
const (
	MinCacheTTLMinutes = 5
	MinPointsFloor     = 0
	MinDedupTTLHours   = 0
	MaxCacheTTLMinutes = int(math.MaxInt64 / int64(time.Minute))
	MaxDedupTTLHours   = int(math.MaxInt64 / int64(time.Hour))
)

// minCacheTTL is MinCacheTTLMinutes as a Duration, which is what Config
// carries.
const minCacheTTL = MinCacheTTLMinutes * time.Minute

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
		fmt.Fprintf(w, "newsfetch: cache_ttl_minutes=%d below minimum %d, using %d\n", badMins, MinCacheTTLMinutes, MinCacheTTLMinutes)
		return silentlyCorrect(c)
	}
	if c.MinPoints < MinPointsFloor {
		bad := c.MinPoints
		c.MinPoints = MinPointsFloor
		fmt.Fprintf(w, "newsfetch: min_points=%d below %d, using %d\n", bad, MinPointsFloor, MinPointsFloor)
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
	if c.DedupWindow < MinDedupTTLHours*time.Hour {
		bad := int(c.DedupWindow / time.Hour)
		c.DedupWindow = MinDedupTTLHours * time.Hour
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
	if c.MinPoints < MinPointsFloor {
		c.MinPoints = MinPointsFloor
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
	if c.DedupWindow < MinDedupTTLHours*time.Hour {
		c.DedupWindow = MinDedupTTLHours * time.Hour
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
// plus everything that went wrong along the way. Feeds are dropped rather
// than repaired when their URL is unusable — there is no sensible guess at
// what a user meant by a relative path — while an out-of-range knob keeps
// the feed and clamps the number, because the URL is the part that carries
// the intent.
//
// A URL that repeats is dropped too, which is the same rule the interactive
// wizard enforces by refusing to add a feed it already holds. A
// hand-edited file was the one surface without it, and left in place a
// repeat costs the user twice: the fan-out builds one FeedSpec per
// occurrence, so a duplicate is a second goroutine against a host that
// already answered, and mergeFollowingStories emits a feed's stories once
// per configured occurrence, so the pool can then render the same article
// as many times as the URL appears. The FIRST occurrence wins, keeping its
// own knobs — a later repeat's max_items or weight is discarded rather
// than merged, because there is no way to tell which of two conflicting
// numbers the user meant.
//
// Equality is the exact configured string, deliberately unnormalised.
// Story.Feed, feeds.json and the validator map are all keyed on the URL as
// written, so two entries this comparison calls different really are two
// different feeds everywhere downstream; making dedup smarter than those
// keys is how a feed gets dropped here and then kept elsewhere.
func splitFeeds(feeds []FeedConfig) ([]FeedConfig, feedIssues) {
	var out []FeedConfig
	var iss feedIssues
	seen := make(map[string]struct{}, len(feeds))
	for _, f := range feeds {
		if reason := feedURLProblem(f.URL); reason != "" {
			iss.dropped++
			iss.dropReasons = appendUnique(iss.dropReasons, reason)
			continue
		}
		if _, dup := seen[f.URL]; dup {
			iss.dropped++
			iss.dropReasons = appendUnique(iss.dropReasons, "duplicate url")
			continue
		}
		seen[f.URL] = struct{}{}
		var reasons []string
		// Zero means unset for both knobs and is left alone; Load turns a
		// value the user actually typed as 0 into a negative so it lands
		// here instead of passing for "absent".
		if f.MaxItems != 0 && (f.MaxItems < defaults.MinFeedItems || f.MaxItems > defaults.MaxFeedItems) {
			f.MaxItems = clampFeedItems(f.MaxItems)
			reasons = append(reasons, fmt.Sprintf("max_items out of [%d, %d]", defaults.MinFeedItems, defaults.MaxFeedItems))
		}
		// The non-finite tests come first and are not redundant: TOML has
		// `nan` and `inf` literals that BurntSushi/toml decodes into a
		// float64 without complaint, and every comparison against NaN is
		// false, so a NaN weight would pass the range check untouched and be
		// written straight back out by --settings as an invalid literal.
		if math.IsNaN(f.Weight) || math.IsInf(f.Weight, 0) || f.Weight < 0 || f.Weight > defaults.MaxFeedWeight {
			f.Weight = clampFeedWeight(f.Weight)
			reasons = append(reasons, fmt.Sprintf("weight out of (0, %g]", defaults.MaxFeedWeight))
		}
		if len(reasons) > 0 {
			// Counted once per feed, not once per knob: the user thinks in
			// feeds, and "3 feeds adjusted" for two bad feeds would be a
			// lie in the direction of alarm.
			iss.adjusted++
			for _, r := range reasons {
				iss.adjustReasons = appendUnique(iss.adjustReasons, r)
			}
		}
		out = append(out, f)
	}
	return out, iss
}

// feedURLProblem returns the reason a feed URL is unusable, or "" when it is
// fine. The check is deliberately shallow — syntax only, never a network
// round trip — because config validation runs on the render hot path, and
// because a reachability failure is a runtime condition rather than a config
// mistake.
func feedURLProblem(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "empty url"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "invalid url"
	}
	if u.Scheme == "" {
		// A relative or protocol-relative reference: there is no host to
		// fetch from, so this reads as a typo rather than a wrong scheme.
		return "invalid url"
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "unsupported url scheme"
	}
	if u.Host == "" {
		return "invalid url"
	}
	return ""
}

// clampFeedItems snaps a typed max_items into [MinFeedItems, MaxFeedItems].
// Callers never pass 0: that is the unset spelling and takes the fetch
// layer's built-in cap instead.
func clampFeedItems(n int) int {
	if n < defaults.MinFeedItems {
		return defaults.MinFeedItems
	}
	if n > defaults.MaxFeedItems {
		return defaults.MaxFeedItems
	}
	return n
}

// clampFeedWeight snaps a typed weight into (0, MaxFeedWeight]. A
// non-positive weight is dropped back to 0 — "no manual override", so the
// feed falls through to its auto cadence weight — rather than clamped up to
// some arbitrary smallest positive number: the range is open at zero, so
// there is no nearest valid value on that side to clamp to.
func clampFeedWeight(f float64) float64 {
	// NaN and ±Inf land here from a `weight = nan` / `weight = inf` config
	// file and must be tested for by name, since NaN compares false against
	// every bound. All three drop the override rather than clamping to the
	// cap: an infinite weight is a typo, not a request for the maximum, and
	// there is no nearest valid value for NaN at all.
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	if f < 0 {
		return 0
	}
	if f > defaults.MaxFeedWeight {
		return defaults.MaxFeedWeight
	}
	return f
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
// the file at once, e.g. "2 feeds dropped: invalid url". Counts rather than
// a list of URLs: the user has the file open and one distinct reason is all
// they need to find them.
func (f feedIssues) warning() string {
	var parts []string
	if f.dropped > 0 {
		parts = append(parts, fmt.Sprintf("%s dropped: %s", plural(f.dropped, "feed"), strings.Join(f.dropReasons, ", ")))
	}
	if f.adjusted > 0 {
		parts = append(parts, fmt.Sprintf("%s adjusted: %s", plural(f.adjusted, "feed"), strings.Join(f.adjustReasons, ", ")))
	}
	return strings.Join(parts, "; ")
}

// plural renders "1 feed" / "2 feeds" so the aggregated warning reads like a
// sentence instead of "1 feed(s)".
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// appendUnique appends s unless list already holds it, preserving
// first-seen order. Three feeds with three unparseable URLs are one problem
// the user fixes once, so the reason is named once.
func appendUnique(list []string, s string) []string {
	for _, existing := range list {
		if existing == s {
			return list
		}
	}
	return append(list, s)
}

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
