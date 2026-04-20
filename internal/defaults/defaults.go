// Package defaults holds hardcoded configuration values for M1. The config
// loader in M2 replaces these without changing their import sites.
package defaults

import (
	"os"
	"time"

	"golang.org/x/term"
)

const (
	// Version is embedded in the cache's cached_by_version field so future
	// schema migrations can detect the producing binary.
	Version = "0.2.0-m2"

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
)

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
