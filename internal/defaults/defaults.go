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

// Sources is the default source list. M4 ships HN-only by default and
// requires the user to opt into Lobste.rs by editing config.toml.
// Reasoning: the mixed HN+Lobste.rs pool has differing score scales and
// volumes; we want to dogfood it before changing the default.
var Sources = []string{"hackernews"}

const (
	// CacheTTL is the stale-while-revalidate window. Reads newer than this
	// render without spawning a background refresh.
	CacheTTL = 30 * time.Minute

	// FetchTimeout bounds one upstream request so a hung network can't
	// keep the background refresh alive forever.
	FetchTimeout = 5 * time.Second
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
