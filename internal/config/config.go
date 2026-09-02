// Package config loads and validates the user's newsfetch configuration.
//
// The config file is optional: a missing file resolves to Defaults() with no
// warning. A malformed file causes Load to return an error and the caller is
// responsible for emitting a one-line warning to stderr and proceeding with
// Defaults(). See the design spec §4.1.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/PietroCoppola/newsfetch/internal/defaults"
	"github.com/PietroCoppola/newsfetch/internal/fetch"
)

// explicitZeroMarker is what Load stores for a per-feed max_items or weight
// the user typed as 0. FeedConfig reserves 0 for "the key was absent", so a
// typed zero decoded straight through would be indistinguishable from an
// absent key and would become a silent default instead of the out-of-range
// warning the user has earned. Any negative value is outside both fields'
// valid ranges, so Validate clamps the marker away — it changes which
// warning the user sees, never the value they end up running with, and no
// package outside config ever observes it.
const explicitZeroMarker = -1

// FeedConfig is one [[following.feeds]] block: a feed URL plus the two
// optional knobs that are TOML-only by design (the wizard surfaces url
// alone).
type FeedConfig struct {
	// URL is the feed document's address. Validate drops a feed whose URL
	// is not an absolute http/https URL with a host.
	URL string
	// MaxItems caps how many of this feed's newest items reach the pool.
	// Zero means unset: it is passed through as a fetch.FeedSpec.MaxItems
	// of 0, and the fetch layer applies its own built-in default of 3
	// (fetch's unexported defaultMaxItems). defaults.DefaultFeedMaxItems
	// mirrors that number for config-side clamping and documentation only
	// - fetch does not read it, so the two constants must stay equal.
	// Validate clamps typed values into
	// [defaults.MinFeedItems, defaults.MaxFeedItems].
	MaxItems int
	// Weight is a manual override that replaces the auto cadence weight
	// entirely. Zero means no override, so the feed keeps whatever weight
	// its publishing rhythm earns. Validate clamps typed values into
	// (0, defaults.MaxFeedWeight].
	Weight float64
}

// NewsConfig is the news pool's internal config: which aggregator front
// pages to fetch. Names must appear in fetch.KnownSourceNames.
type NewsConfig struct {
	Aggregators []string
}

// FollowingConfig is the following pool's internal config: the RSS/Atom
// feeds the user subscribes to, in the order they wrote them.
type FollowingConfig struct {
	Feeds []FeedConfig
}

// Config is the merged runtime settings: compile-time defaults overlaid with
// values from the TOML file and any applicable CLI overrides.
type Config struct {
	Topics    []string      // nil → no topic filter
	Style     string        // "boxed" | "minimal" | "json" | "statusline" (statusline is flag-only; Validate rejects it from config)
	CacheTTL  time.Duration // derived from cache_ttl_minutes
	MinPoints int
	// Count is the number of stories rendered from the news pool per
	// invocation, 1..MaxCount. Values outside the range are clamped by
	// Validate. --count keeps meaning this field and no other.
	Count int
	// FollowingCount is the same knob for the following pool. Each pool
	// selects independently, so the two never trade slots.
	FollowingCount int
	// Pools is the enable list. Names must appear in defaults.KnownPools;
	// Validate drops unknowns and falls back to defaults.Pools() if nothing
	// valid remains. An enabled pool with an empty internal config
	// contributes nothing rather than erroring.
	Pools []string
	// PoolOrder is the vertical stacking order of the rendered boxes.
	// Validate normalises it into a permutation of Pools, silently, so
	// callers can walk it without re-checking enablement.
	PoolOrder []string
	// News and Following hold each pool's internal config. They are kept
	// even while their pool is disabled (persist-don't-clear): removing a
	// pool from Pools must not cost the user their feed list.
	News      NewsConfig
	Following FollowingConfig
	// TickerMarker is the symbol prefixing each non-hero entry in
	// multi-story renders. Names match render.KnownTickerMarkers. It
	// applies across every pool — per-pool ticker overrides are a
	// deliberate non-feature.
	TickerMarker string
	// TickerBoxed selects between one connected box (true) and a hero box
	// with plain ticker lines beneath (false). Only takes effect when
	// Style == "boxed" and a pool's count is above 1.
	TickerBoxed bool
	// DedupWindow is the dedup window. A story rendered within the
	// last DedupWindow is filtered out of the candidate pool, then
	// ages back in. Zero means dedup is disabled entirely (every story
	// in the cache is always eligible). Validate clamps negative values
	// to zero with a warning.
	DedupWindow time.Duration
}

// Defaults returns the compile-time fallback config. Validate is a no-op on
// this value by construction.
//
// PoolOrder holds the ENABLED pools in compile-time order rather than
// defaults.PoolOrder() itself, because Validate normalises pool_order into a
// permutation of Pools: seeding it with a disabled pool's name would make
// Validate rewrite its own defaults and break the no-op invariant above.
func Defaults() Config {
	return Config{
		Topics:         nil,
		Style:          defaults.Style,
		CacheTTL:       defaults.CacheTTL,
		MinPoints:      defaults.MinPoints,
		Count:          defaults.Count,
		FollowingCount: defaults.FollowingCount,
		Pools:          defaults.Pools(),
		PoolOrder:      defaults.Pools(),
		News:           NewsConfig{Aggregators: defaults.Sources()},
		Following:      FollowingConfig{},
		TickerMarker:   defaults.TickerMarker,
		TickerBoxed:    defaults.TickerBoxed,
		DedupWindow:    defaults.DedupWindow,
	}
}

// Path returns the absolute path to the config file. XDG_CONFIG_HOME is
// honoured only when it is an absolute path; otherwise it falls back to
// $HOME/.config/newsfetch/config.toml. Mirrors the M1 cache.Path() contract.
func Path() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "newsfetch", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve config path: %w", err)
	}
	return filepath.Join(home, ".config", "newsfetch", "config.toml"), nil
}

// Load reads and parses the config file at path. It returns:
//
//   - (Defaults(), nil) if the file does not exist (normal first-run case).
//   - (Defaults(), err) if the file exists but fails to parse. The caller
//     is responsible for emitting a warning and proceeding with Defaults().
//   - (merged, nil) where merged is Defaults() overlaid with the fields
//     actually present in the file. Unknown keys are silently ignored.
//
// Integer fields present in the file always override (including zero), so
// Validate can see and correct intentionally out-of-range values. Missing
// fields keep their default. Load never writes: the legacy `sources` key is
// aliased in memory, not migrated on disk.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Defaults(), nil
		}
		return Defaults(), fmt.Errorf("read config: %w", err)
	}
	var raw struct {
		Topics          []string `toml:"topics"`
		Style           string   `toml:"style"`
		CacheTTLMinutes int      `toml:"cache_ttl_minutes"`
		MinPoints       int      `toml:"min_points"`
		Sources         []string `toml:"sources"`
		Pools           []string `toml:"pools"`
		PoolOrder       []string `toml:"pool_order"`
		Count           int      `toml:"count"`
		FollowingCount  int      `toml:"following_count"`
		TickerMarker    string   `toml:"ticker_marker"`
		TickerBoxed     bool     `toml:"ticker_boxed"`
		DedupTTLHours   int      `toml:"dedup_ttl_hours"`
		News            struct {
			Aggregators []string `toml:"aggregators"`
		} `toml:"news"`
		Following struct {
			// MaxItems and Weight are pointers because
			// toml.MetaData.IsDefined walks one map per key segment and
			// cannot index an array-of-tables element: the IsDefined
			// idiom every top-level field below uses simply does not
			// reach a [[following.feeds]] member. The pointer is the
			// only thing separating "the user wrote 0" from "the user
			// wrote nothing".
			Feeds []struct {
				URL      string   `toml:"url"`
				MaxItems *int     `toml:"max_items"`
				Weight   *float64 `toml:"weight"`
			} `toml:"feeds"`
		} `toml:"following"`
	}
	meta, err := toml.Decode(string(data), &raw)
	if err != nil {
		return Defaults(), fmt.Errorf("parse config: %w", err)
	}
	cfg := Defaults()
	if meta.IsDefined("topics") {
		cfg.Topics = raw.Topics
	}
	if meta.IsDefined("style") {
		cfg.Style = raw.Style
	}
	if meta.IsDefined("cache_ttl_minutes") {
		cfg.CacheTTL = time.Duration(raw.CacheTTLMinutes) * time.Minute
	}
	if meta.IsDefined("min_points") {
		cfg.MinPoints = raw.MinPoints
	}
	if meta.IsDefined("count") {
		cfg.Count = raw.Count
	}
	if meta.IsDefined("following_count") {
		cfg.FollowingCount = raw.FollowingCount
	}
	if meta.IsDefined("ticker_marker") {
		cfg.TickerMarker = raw.TickerMarker
	}
	if meta.IsDefined("ticker_boxed") {
		cfg.TickerBoxed = raw.TickerBoxed
	}
	if meta.IsDefined("dedup_ttl_hours") {
		cfg.DedupWindow = time.Duration(raw.DedupTTLHours) * time.Hour
	}

	// Pool enablement and the news pool's aggregator list. `sources` is the
	// M4 spelling of "the news pool's aggregators" and is aliased here at
	// read time — never written back, because a BurntSushi/toml round trip
	// is lossy to comments and a hand-written config file is not ours to
	// reformat.
	//
	// Aliased in TOML but deliberately NOT in the JSON wizard readers: the
	// TOML alias protects config.toml files that exist on disk today, while
	// a JSON alias would protect scripts that do not — newsfetch has no
	// scripted --init/--settings callers. The asymmetry is correctly
	// scoped, not an oversight; do not "fix" it by adding a JSON alias.
	switch {
	case meta.IsDefined("pools"):
		// A file that speaks the pool vocabulary is authoritative, so a
		// leftover `sources` line is ignored outright rather than
		// half-merged into the news pool.
		cfg.Pools = raw.Pools
		if meta.IsDefined("news", "aggregators") {
			cfg.News.Aggregators = raw.News.Aggregators
		}
	case meta.IsDefined("sources"):
		cfg.Pools = defaults.Pools()
		if meta.IsDefined("news", "aggregators") {
			// Both spellings present with `pools` absent: the explicit
			// [news] aggregators wins and `sources` is ignored, silently.
			// The newer key is the one the user most recently learned,
			// and Load has no writer to warn through — a per-invocation
			// nag on every terminal open is the wrong trade.
			cfg.News.Aggregators = raw.News.Aggregators
		} else {
			cfg.News.Aggregators = raw.Sources
		}
	default:
		if meta.IsDefined("news", "aggregators") {
			cfg.News.Aggregators = raw.News.Aggregators
		}
	}
	if meta.IsDefined("pool_order") {
		cfg.PoolOrder = raw.PoolOrder
	} else if meta.IsDefined("pools") {
		// The file chose its pools but not their order, so the
		// compile-time order applies rather than Defaults()'s single-pool
		// value. Validate filters it down to the enabled set, which is how
		// enabling `following` by hand still lands it in the prime slot
		// the design gives it.
		cfg.PoolOrder = defaults.PoolOrder()
	}

	var feeds []FeedConfig
	for _, rf := range raw.Following.Feeds {
		fc := FeedConfig{URL: rf.URL}
		if rf.MaxItems != nil {
			fc.MaxItems = *rf.MaxItems
			if fc.MaxItems == 0 {
				fc.MaxItems = explicitZeroMarker
			}
		}
		if rf.Weight != nil {
			fc.Weight = *rf.Weight
			if fc.Weight == 0 {
				fc.Weight = explicitZeroMarker
			}
		}
		feeds = append(feeds, fc)
	}
	// Assigned unconditionally: a file with no [[following.feeds]] blocks
	// leaves feeds nil, which is exactly what Defaults() already holds.
	cfg.Following.Feeds = feeds
	return cfg, nil
}

// FeedURLs returns the configured feed URLs in config order. It is the one
// place that list is derived: feedstate.Update garbage-collects every feed
// absent from the list it is handed, so a second derivation that disagreed
// even slightly is how a user's four weeks of cadence history gets deleted.
func (c Config) FeedURLs() []string {
	if len(c.Following.Feeds) == 0 {
		return nil
	}
	urls := make([]string, 0, len(c.Following.Feeds))
	for _, f := range c.Following.Feeds {
		urls = append(urls, f.URL)
	}
	return urls
}

// FeedSpecs returns the fetch-layer view of the configured feeds. MaxItems
// passes through unchanged, including the zero: fetch.FeedSpec already reads
// a non-positive MaxItems as "use the built-in cap", so translating it here
// would put the same default in two places that could drift apart.
func (c Config) FeedSpecs() []fetch.FeedSpec {
	if len(c.Following.Feeds) == 0 {
		return nil
	}
	specs := make([]fetch.FeedSpec, 0, len(c.Following.Feeds))
	for _, f := range c.Following.Feeds {
		specs = append(specs, fetch.FeedSpec{URL: f.URL, MaxItems: f.MaxItems})
	}
	return specs
}

// PoolEnabled reports whether name appears in Pools. Callers use it instead
// of scanning Pools themselves so "enabled" has exactly one definition.
func (c Config) PoolEnabled(name string) bool {
	for _, p := range c.Pools {
		if p == name {
			return true
		}
	}
	return false
}

// FollowingActive reports whether the following pool would actually
// contribute anything: enabled AND holding at least one feed. Callers gate
// the cadence-weight computation on it so a user with no feeds pays nothing
// for a pool they never turned on.
func (c Config) FollowingActive() bool {
	return c.PoolEnabled("following") && len(c.Following.Feeds) > 0
}

// NewsActive reports whether the news pool would actually contribute
// anything: enabled AND holding at least one aggregator. It exists because
// an empty aggregator list is legal and silent (R-8), so "the news pool is
// enabled" and "the news pool can produce anything" are two different
// questions and callers almost always mean the second. Gating on
// PoolEnabled alone lets the render path serve stories from a cache written
// before the user emptied the list, while marking the pool stale forever and
// asking for a refresh that skips it.
func (c Config) NewsActive() bool {
	return c.PoolEnabled("news") && len(c.News.Aggregators) > 0
}
