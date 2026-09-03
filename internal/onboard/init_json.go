package onboard

import (
	"encoding/json"
	"errors"
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

// ReadInitJSON parses [Answers] from r as JSON. Used by --init when stdin
// is not a TTY so the install flow is scriptable without trying to render
// an interactive wizard into a pipe. Schema:
//
//	{ "topics": ["rust"], "style": "boxed" }                                  // basic
//	{ "topics": ["rust"], "style": "boxed",
//	  "pools": ["news", "following"], "pool_order": ["following", "news"],
//	  "count": 2, "following_count": 1,
//	  "ticker_marker": "branch", "ticker_boxed": true,
//	  "cache_ttl_minutes": 45, "min_points": 10, "dedup_ttl_hours": 3,
//	  "news": {"aggregators": ["hackernews", "lobsters"]},
//	  "following": {"feeds": [
//	    {"url": "https://drewdevault.com/blog/index.xml"},
//	    {"url": "https://blog.cloudflare.com/rss/", "max_items": 2, "weight": 0.3}
//	  ]} }                                                                    // full schema
//
// topics and style are required; a missing field is an error rather than a
// silent default — a script should be explicit about what it's installing,
// and a half-specified config is harder to debug than a clean rejection.
//
// Everything else is OPTIONAL on --init: the --init wizard intentionally
// surfaces only the basics, so JSON callers also get to skip the rest. When
// present the values are validated; when absent they take the compile-time
// defaults, except news.aggregators, following.feeds and pool_order, which
// stay nil so the config writer omits them entirely (future default changes
// then flow through to the user). cache_ttl_minutes, min_points, and
// dedup_ttl_hours follow the same "absent takes the compile-time default"
// rule as count and following_count — see settings_json.go's ReadSettingsJSON
// for the sibling behaviour on --settings, where an absent value inherits
// the CALLER's current configuration instead. Unknown JSON fields are
// rejected at every nesting depth.
//
// There is NO "sources" key and no alias for one (ruling R-4).
// DisallowUnknownFields rejects it by name, which is the entire migration
// story for a path nobody scripts. The TOML loader DOES still alias a
// legacy top-level `sources` onto pools + [news] aggregators, and that
// asymmetry is correct rather than an oversight: the TOML alias protects
// config.toml files sitting on real disks today, while a JSON alias would
// only protect scripts that do not exist. Do not "fix" the inconsistency.
func ReadInitJSON(r io.Reader) (Answers, error) {
	var raw struct {
		Topics          *[]string      `json:"topics"`
		Style           *string        `json:"style"`
		Pools           *[]string      `json:"pools"`
		PoolOrder       *[]string      `json:"pool_order"`
		Count           *int           `json:"count"`
		FollowingCount  *int           `json:"following_count"`
		TickerMarker    *string        `json:"ticker_marker"`
		TickerBoxed     *bool          `json:"ticker_boxed"`
		CacheTTLMinutes *int           `json:"cache_ttl_minutes"`
		MinPoints       *int           `json:"min_points"`
		DedupTTLHours   *int           `json:"dedup_ttl_hours"`
		News            *newsJSON      `json:"news"`
		Following       *followingJSON `json:"following"`
	}
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return Answers{}, fmt.Errorf("decode --init JSON: %w", err)
	}
	if raw.Topics == nil {
		return Answers{}, errors.New(`--init JSON: missing required field "topics" (array of strings; [] is allowed)`)
	}
	if raw.Style == nil {
		return Answers{}, errors.New(`--init JSON: missing required field "style" (boxed | minimal | json)`)
	}
	if err := validateStyle("--init", *raw.Style); err != nil {
		return Answers{}, err
	}
	a := Answers{
		Topics:          *raw.Topics,
		Style:           *raw.Style,
		Pools:           defaults.Pools(),
		Count:           defaults.Count,
		FollowingCount:  defaults.FollowingCount,
		TickerMarker:    defaults.TickerMarker,
		TickerBoxed:     defaults.TickerBoxed,
		CacheTTLMinutes: int(defaults.CacheTTL / time.Minute),
		MinPoints:       defaults.MinPoints,
		DedupTTLHours:   int(defaults.DedupWindow / time.Hour),
	}
	if raw.Pools != nil {
		if err := validatePools("--init", *raw.Pools); err != nil {
			return Answers{}, err
		}
		a.Pools = *raw.Pools
	}
	// Must run AFTER the a.Pools block above, not before: validatePoolOrder
	// checks each entry against a.Pools, and json.Decoder has already
	// populated the whole raw struct by the time either block runs, so the
	// order of keys in the input JSON has no bearing on the outcome — only
	// the order of these two blocks in the source does.
	if raw.PoolOrder != nil {
		if err := validatePoolOrder("--init", *raw.PoolOrder, a.Pools); err != nil {
			return Answers{}, err
		}
		a.PoolOrder = *raw.PoolOrder
	}
	if raw.Count != nil {
		if err := validateCount("--init", *raw.Count); err != nil {
			return Answers{}, err
		}
		a.Count = *raw.Count
	}
	if raw.FollowingCount != nil {
		if err := validateFollowingCount("--init", *raw.FollowingCount); err != nil {
			return Answers{}, err
		}
		a.FollowingCount = *raw.FollowingCount
	}
	if raw.TickerMarker != nil {
		if err := validateTickerMarker("--init", *raw.TickerMarker); err != nil {
			return Answers{}, err
		}
		a.TickerMarker = *raw.TickerMarker
	}
	if raw.TickerBoxed != nil {
		a.TickerBoxed = *raw.TickerBoxed
	}
	// No range validation here: cache_ttl_minutes, min_points, and
	// dedup_ttl_hours have no exported floor to check against (config.Validate
	// owns minCacheTTL privately), and a hand-edited config.toml already
	// reaches disk with unvalidated values for these three today — Validate
	// clamps and warns at the next render either way. Carrying the raw int
	// through here keeps that one behaviour rather than inventing a second,
	// JSON-only floor that could drift from config.Validate's.
	if raw.CacheTTLMinutes != nil {
		a.CacheTTLMinutes = *raw.CacheTTLMinutes
	}
	if raw.MinPoints != nil {
		a.MinPoints = *raw.MinPoints
	}
	if raw.DedupTTLHours != nil {
		a.DedupTTLHours = *raw.DedupTTLHours
	}
	if raw.News != nil && raw.News.Aggregators != nil {
		if err := validateSources("--init", *raw.News.Aggregators); err != nil {
			return Answers{}, err
		}
		a.NewsAggregators = *raw.News.Aggregators
	}
	if raw.Following != nil && raw.Following.Feeds != nil {
		feeds, err := feedsFromJSON("--init", *raw.Following.Feeds)
		if err != nil {
			return Answers{}, err
		}
		if err := validateFeeds("--init", feeds); err != nil {
			return Answers{}, err
		}
		a.Feeds = feeds
	}
	if err := validatePoolContent("--init", a); err != nil {
		return Answers{}, err
	}
	return a, nil
}

// validateStyle rejects style values outside the known set. Shared between
// the --init and --settings JSON readers so the error message format is
// consistent.
func validateStyle(flag, style string) error {
	switch style {
	case "boxed", "minimal", "json":
		return nil
	default:
		return fmt.Errorf(`%s JSON: invalid style %q (must be boxed | minimal | json)`, flag, style)
	}
}

// validateSources rejects unknown aggregator names for the [news] pool.
// Mirrors the guarantees config.Validate gives at config-load time, but
// enforces them at JSON-parse time so a scripted user gets fail-loud
// feedback rather than a warning + fallback at the next render.
//
// An EMPTY list is deliberately accepted (rulings R-8 and R-39): the
// design's validation table makes an empty aggregator list valid, and the
// JSON boundary must not disagree with the TOML one about what the same
// list means. The case an empty list can genuinely break — every enabled
// pool left with nothing in it — is caught by validatePoolContent after the
// merge, where the whole picture is visible. Do not put an emptiness check
// back here; it would reject payloads that config.Validate accepts.
func validateSources(flag string, names []string) error {
	for _, n := range names {
		if !knownSourceName(n) {
			return fmt.Errorf("%s JSON: unknown aggregator %q (valid: %v)", flag, n, fetch.KnownSourceNames())
		}
	}
	return nil
}

func knownSourceName(name string) bool {
	for _, k := range fetch.KnownSourceNames() {
		if k == name {
			return true
		}
	}
	return false
}

// validateCount rejects out-of-range values. Mirrors config.Validate's
// clamp-and-warn but at JSON-parse time so scripted users get fail-loud
// feedback instead of a clamped value at next render.
func validateCount(flag string, n int) error {
	if n < 1 || n > defaults.MaxCount {
		return fmt.Errorf("%s JSON: count=%d out of [1, %d]", flag, n, defaults.MaxCount)
	}
	return nil
}

// validateTickerMarker rejects unknown marker names. The known set is
// owned by render.KnownTickerMarkers — single source of truth.
func validateTickerMarker(flag, name string) error {
	markers := render.KnownTickerMarkers()
	for _, m := range markers {
		if string(m) == name {
			return nil
		}
	}
	known := make([]string, len(markers))
	for i, m := range markers {
		known[i] = string(m)
	}
	return fmt.Errorf("%s JSON: unknown ticker_marker %q (valid: %v)", flag, name, known)
}

// newsJSON, followingJSON and feedJSON mirror the [news] and
// [[following.feeds]] TOML blocks. They are named types rather than inline
// anonymous structs so ReadInitJSON and ReadSettingsJSON decode an
// identical shape — a divergence between the two schemas is drift nobody
// would notice until a script broke on one flag but not the other.
//
// They must stay STRUCTS. json.Decoder.DisallowUnknownFields is consulted
// at every nesting depth, but only for struct targets; switching any of
// these to map[string]any or json.RawMessage would silently re-admit
// unknown sub-keys.
type newsJSON struct {
	Aggregators *[]string `json:"aggregators"`
}

type followingJSON struct {
	Feeds *[]feedJSON `json:"feeds"`
}

type feedJSON struct {
	URL      *string  `json:"url"`
	MaxItems *int     `json:"max_items"`
	Weight   *float64 `json:"weight"`
}

// feedsFromJSON converts decoded feed objects into Answers.Feed. url is
// required; max_items and weight are carried through as the pointers they
// arrived as, so a knob the caller omitted stays omitted when the config is
// rewritten rather than being pinned to a value the caller never chose.
//
// An explicitly empty feeds array converts to nil, which the config writer
// reads as "emit no feed tables". That is the intended way to clear every
// feed: persist-don't-clear applies to an OMITTED key, not to one the
// caller deliberately set to [].
func feedsFromJSON(flag string, raw []feedJSON) ([]Feed, error) {
	feeds := make([]Feed, 0, len(raw))
	for i, f := range raw {
		if f.URL == nil {
			return nil, fmt.Errorf(`%s JSON: following.feeds[%d]: missing required field "url"`, flag, i)
		}
		feeds = append(feeds, Feed{URL: *f.URL, MaxItems: f.MaxItems, Weight: f.Weight})
	}
	if len(feeds) == 0 {
		return nil, nil
	}
	return feeds, nil
}

// validatePools rejects an empty list, unknown pool names, and duplicates.
// The known set is owned by defaults.KnownPools — the same registry the
// config validator and the box labels read, so a pool cannot be spellable
// in one place and not another.
func validatePools(flag string, names []string) error {
	if len(names) == 0 {
		return fmt.Errorf("%s JSON: pools must be non-empty", flag)
	}
	seen := make(map[string]struct{}, len(names))
	for _, n := range names {
		if !knownPoolName(n) {
			return fmt.Errorf("%s JSON: unknown pool %q (valid: %v)", flag, n, defaults.KnownPools())
		}
		if _, dup := seen[n]; dup {
			return fmt.Errorf("%s JSON: duplicate pool %q", flag, n)
		}
		seen[n] = struct{}{}
	}
	return nil
}

func knownPoolName(name string) bool {
	for _, k := range defaults.KnownPools() {
		if k == name {
			return true
		}
	}
	return false
}

// validatePoolOrder rejects unknown names, duplicates, and entries naming a
// pool that is not enabled. A PARTIAL order is accepted: config.Validate
// appends the missing pools in compile-time order, so demanding a full
// permutation here would make callers restate an ordering the loader
// already knows how to complete.
func validatePoolOrder(flag string, order, pools []string) error {
	enabled := make(map[string]struct{}, len(pools))
	for _, p := range pools {
		enabled[p] = struct{}{}
	}
	seen := make(map[string]struct{}, len(order))
	for _, p := range order {
		if !knownPoolName(p) {
			return fmt.Errorf("%s JSON: unknown pool %q in pool_order (valid: %v)", flag, p, defaults.KnownPools())
		}
		if _, dup := seen[p]; dup {
			return fmt.Errorf("%s JSON: duplicate pool_order entry %q", flag, p)
		}
		seen[p] = struct{}{}
		if _, ok := enabled[p]; !ok {
			return fmt.Errorf("%s JSON: pool_order entry %q is not an enabled pool (pools: %v)", flag, p, pools)
		}
	}
	return nil
}

// validateFollowingCount rejects out-of-range values. Shares MaxCount with
// the news pool's count: the hero-plus-ticker format stops reading as
// intentional past the same bound whichever pool is drawing it.
func validateFollowingCount(flag string, n int) error {
	if n < 1 || n > defaults.MaxCount {
		return fmt.Errorf("%s JSON: following_count=%d out of [1, %d]", flag, n, defaults.MaxCount)
	}
	return nil
}

// validateFeeds enforces the per-feed rules at parse time so a scripted
// caller gets a loud rejection naming the offending index, rather than the
// clamp-and-warn the TOML loader applies to a hand-edited file. The index
// is in the message because a feed list is usually generated, and "which
// one" is the only question the caller will have.
func validateFeeds(flag string, feeds []Feed) error {
	for i, f := range feeds {
		if err := ValidateFeedURL(f.URL); err != nil {
			return fmt.Errorf("%s JSON: following.feeds[%d]: %w", flag, i, err)
		}
		if f.MaxItems != nil && (*f.MaxItems < defaults.MinFeedItems || *f.MaxItems > defaults.MaxFeedItems) {
			return fmt.Errorf("%s JSON: following.feeds[%d]: max_items=%d out of [%d, %d]",
				flag, i, *f.MaxItems, defaults.MinFeedItems, defaults.MaxFeedItems)
		}
		// NaN must be rejected explicitly. Every comparison against NaN is
		// false, so `<= 0 || > MaxFeedWeight` lets it through untouched —
		// and JSON reaches this with an ordinary number literal only, but
		// the same Feed values arrive from --settings' round trip, where a
		// TOML `weight = nan` decodes to a real NaN. A NaN weight
		// multiplies every score in the ranker into NaN and has no valid
		// TOML literal to be written back as.
		if f.Weight != nil {
			if math.IsNaN(*f.Weight) || math.IsInf(*f.Weight, 0) {
				return fmt.Errorf("%s JSON: following.feeds[%d]: weight must be a finite number in (0, %v]",
					flag, i, defaults.MaxFeedWeight)
			}
			if *f.Weight <= 0 || *f.Weight > defaults.MaxFeedWeight {
				return fmt.Errorf("%s JSON: following.feeds[%d]: weight=%v out of (0, %v]",
					flag, i, *f.Weight, defaults.MaxFeedWeight)
			}
		}
	}
	return nil
}

// validatePoolContent rejects the one combination neither per-field
// validator can see: every enabled pool empty, so the render would produce
// nothing at all. Ruling R-39. It is shared by both readers and must run
// LAST in each, after the payload has been merged with whatever it
// inherits — in --settings the emptiness is usually not in the payload at
// all, and appears only once an omitted news.aggregators has fallen back to
// a current that is itself empty.
//
// TOML reaches this state too and CLAMPS it: config.Validate restores both
// the default pools and the default aggregators (ruling R-9) and warns. JSON
// does not clamp — "no clamp-and-warn at JSON-parse time; JSON callers fail
// loud" — so a scripted caller is told what is wrong instead of quietly
// getting a config they did not ask for.
//
// A nil list is NOT an empty list. Nil means the caller omitted the key, so
// the writer omits the table and config.Load supplies the compile-time
// default: an omitted news.aggregators yields defaults.Sources(), which is
// non-empty. Only an explicitly-present-and-empty list counts as empty.
// Feeds have no compile-time default, so nil and empty are the same for
// them.
func validatePoolContent(flag string, a Answers) error {
	var empties []string
	for _, p := range a.Pools {
		switch p {
		case "news":
			// Nil inherits defaults.Sources(); [] is genuinely empty.
			if a.NewsAggregators == nil || len(a.NewsAggregators) > 0 {
				return nil
			}
			empties = append(empties, "news is enabled with no aggregators")
		case "following":
			if len(a.Feeds) > 0 {
				return nil
			}
			empties = append(empties, "following is enabled with no feeds")
		default:
			// An unknown name cannot reach here (validatePools ran first),
			// but a pool added to the registry without a case above would.
			// Treat it as content-bearing rather than inventing a failure.
			return nil
		}
	}
	if len(empties) == 0 {
		return nil
	}
	return fmt.Errorf("%s JSON: every enabled pool is empty, so nothing would render (%s); give news.aggregators at least one name, or following.feeds at least one url, or drop the empty pool from pools",
		flag, strings.Join(empties, "; "))
}

// ValidateFeedURL reports whether raw is usable as a feed address: a
// parseable absolute URL with an http or https scheme and a host.
//
// Exported because the interactive --settings wizard uses it as the
// validator on its add-feed input, and wizard form construction is not
// unit-tested by project policy — keeping the rule here is what lets a test
// reach it at all.
//
// Syntax only. There is deliberately no reachability check: the wizard runs
// on whatever network the user happens to have, and blocking the add-feed
// loop on a slow or offline host to reject a URL that is perfectly valid is
// the worse failure. A genuinely unreachable feed surfaces at refresh time.
func ValidateFeedURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("feed url is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("feed url %q could not be parsed: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("feed url %q must use the http or https scheme (got %q)", raw, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("feed url %q has no host", raw)
	}
	return nil
}
