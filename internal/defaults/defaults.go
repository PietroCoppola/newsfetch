// Package defaults holds hardcoded configuration values for M1. The config
// loader in M2 replaces these without changing their import sites.
package defaults

import (
	"os"
	"time"

	"golang.org/x/term"
)

// Version identifies the running binary. Set via -ldflags
//
//	-X github.com/PietroCoppola/newsfetch/internal/defaults.Version={{.Version}}
//
// at release-build time (see .goreleaser.yaml). Defaults to "dev" so
// `go install` builds — which don't pass ldflags — get an honest marker
// rather than a stale string baked at source-edit time. Showing
// "newsfetch/dev" in upstream User-Agent logs and the cache's
// cached_by_version field is the right signal for unreleased builds:
// site operators can tell exactly what's running.
//
// Declared as var (not const) so ldflags can override the value. All
// reads are runtime, so a const-vs-var swap is invisible to callers.
var Version = "dev"

const (
	// BoxWidth is the fallback render width used when the terminal size
	// can't be detected (non-TTY stdout, GetSize error) or when the
	// detected size falls outside the clamp range. See TermWidth.
	BoxWidth = 80

	// NumStories is the per-fetch upper bound.
	NumStories = 30

	// MinPoints filters noise from the HN firehose (applied as points>=N).
	MinPoints = 50

	// FallbackMessage renders when the cache is missing and the fetcher
	// fails - for example, offline on first run.
	FallbackMessage = "no fresh news — check your connection"

	// RankPoolSize is the top-N candidate window for stochastic selection in
	// the ranker. M2's default.
	RankPoolSize = 10

	// Style is the default render mode when no config or flag overrides it.
	Style = "boxed"

	// Count is the default number of stories rendered per invocation.
	// Bounded by MaxCount; values above are rejected as a friendly error.
	Count = 1

	// MaxCount caps multi-story renders. Hero+ticker stops feeling
	// intentional beyond this and turns into a list, which isn't what the
	// format is for.
	MaxCount = 4

	// TickerMarker is the default symbol for ticker entries when more than
	// one story renders. Names mirror render.TickerMarker.
	TickerMarker = "dot"

	// TickerBoxed controls whether multi-story renders draw a single
	// outer box around hero plus tickers (true) or render the hero in its
	// own box with ticker lines beneath (false).
	TickerBoxed = false
)

const (
	// MaxFeedWeight is the upper bound on a following-pool feed's ranking
	// multiplier and the single home for that number. internal/feedstate
	// caps its computed cadence weight here, and internal/config validates
	// a manually configured per-feed weight against the same constant, so
	// the automatic and manual paths cannot drift apart — which is exactly
	// what two hardcoded 5.0s in two packages would eventually do.
	MaxFeedWeight = 5.0

	// DefaultFeedMaxItems is how many of a feed's newest items reach the
	// pool when the feed sets no max_items of its own. Three keeps a busy
	// publisher from filling the candidate pool before the cadence weight
	// gets a say, while still giving a weekly blog more than one shot.
	DefaultFeedMaxItems = 3

	// MinFeedItems and MaxFeedItems bound a per-feed max_items override.
	// Zero would silently disable a feed the user deliberately configured,
	// which is what removing it from the config is for; past ten a single
	// feed crowds out every other feed in the pool.
	MinFeedItems = 1
	MaxFeedItems = 10
)

// Sources returns the default source list as a fresh copy per call,
// matching the fetch.KnownSourceNames registry convention (Go cannot
// declare const slices). M4 ships HN-only by default and requires the
// user to opt into Lobste.rs by editing config.toml. Reasoning: the
// mixed HN+Lobste.rs pool has differing score scales and volumes; we
// want to dogfood it before changing the default.
func Sources() []string {
	return []string{"hackernews"}
}

const (
	// FollowingCount is the default number of stories rendered from the
	// following pool. It is a separate knob from Count rather than a reuse
	// of it because the two answer different questions: Count says how much
	// of an aggregator firehose to sample, while a feed the user chose
	// themselves earns a slot even from someone who only ever wants one
	// headline.
	FollowingCount = 1
)

const (
	// FollowingFetchTimeout bounds one whole following-pool fan-out. It is
	// deliberately larger than FetchTimeout: the fan-out runs only inside
	// the detached refresh process and never on a render path, and a parent
	// smaller than FollowingPerFeedTimeout would clip every per-feed budget
	// to nothing.
	FollowingFetchTimeout = 15 * time.Second

	// FollowingPerFeedTimeout bounds one feed request inside the fan-out so
	// a single slow host cannot spend the whole parent budget and starve
	// the feeds behind it.
	FollowingPerFeedTimeout = 10 * time.Second
)

// Pools returns the default pool enable list as a fresh copy per call,
// matching the Sources registry convention. Following ships disabled
// because a first-run user has configured no feeds, and an enabled pool
// with an empty internal config renders nothing anyway — enabling it by
// default would buy startup work and no output.
func Pools() []string {
	return []string{"news"}
}

// PoolOrder returns the compile-time vertical stacking order as a fresh
// copy per call. Following leads because feeds the user picked deserve the
// prime slot over an aggregator front page. Every name in KnownPools must
// appear here: config's pool_order normalisation fills in the pools a user
// left out by walking this list, so a pool missing from it would be enabled
// and never rendered.
func PoolOrder() []string {
	return []string{"following", "news"}
}

// KnownPools lists every pool name the binary recognises, as a fresh copy
// per call. It is deliberately a separate registry from
// fetch.KnownSourceNames: following is a pool, not an aggregator, and
// keeping the two lists apart is what makes aggregators = ["following"]
// impossible to spell.
func KnownPools() []string {
	return []string{"news", "following"}
}

// PoolLabel returns the box header shown for a pool, or "" for a name that
// is not a known pool. Labels live in defaults rather than in render
// because config imports render, so render can never import config;
// defaults is the leaf package both sides already depend on.
func PoolLabel(pool string) string {
	switch pool {
	case "news":
		return "News"
	case "following":
		return "Following"
	default:
		return ""
	}
}

const (
	// CacheTTL is the stale-while-revalidate window. Reads newer than this
	// render without spawning a background refresh.
	CacheTTL = 30 * time.Minute

	// FetchTimeout bounds one upstream request so a hung network can't
	// keep the background refresh alive forever.
	FetchTimeout = 5 * time.Second

	// DedupWindow is the default dedup window: a story rendered within
	// the last DedupWindow is filtered out of the candidate pool, after
	// which it ages back in. The default sits between the cache TTL
	// (30 min — too short, a story would re-appear in the next refresh)
	// and a day (too long — yesterday's top item shouldn't still be
	// blocked). Configurable via dedup_ttl_hours; zero disables the
	// time gate entirely so every cached story is always eligible.
	DedupWindow = 6 * time.Hour
)

// TermWidth reports a render width for the boxed style. It consults the
// underlying terminal via x/term.GetSize on stdout; if that fails (stdout
// is a pipe, redirect, or the call errors for any other reason), it
// returns fallback. Detected widths are clamped: below 40 collapses to
// fallback, above 100 clamps to 100, inside the range passes through.
func TermWidth(fallback int) int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return fallback
	}
	return clampWidth(w, fallback)
}

// clampWidth enforces the boxed-render width policy. Kept package-private
// so the boundary logic can be tested directly without a real TTY.
func clampWidth(w, fallback int) int {
	if w < 40 {
		return fallback
	}
	if w > 100 {
		return 100
	}
	return w
}
