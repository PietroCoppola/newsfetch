// Package defaults holds hardcoded configuration values for M1. The config
// loader in M2 replaces these without changing their import sites.
package defaults

import "time"

const (
	// Version is embedded in the cache's cached_by_version field so future
	// schema migrations can detect the producing binary.
	Version = "0.1.0-m1"

	// BoxWidth caps the boxed render. M1 hardcodes it; M2 will detect the
	// actual terminal width.
	BoxWidth = 80

	// NumStories is the per-fetch upper bound.
	NumStories = 30

	// MinPoints filters noise from the HN firehose (applied as points>=N).
	MinPoints = 50

	// FallbackMessage renders when the cache is missing and the fetcher
	// fails - for example, offline on first run.
	FallbackMessage = "no fresh news — check your connection"
)

const (
	// CacheTTL is the stale-while-revalidate window. Reads newer than this
	// render without spawning a background refresh.
	CacheTTL = 30 * time.Minute

	// FetchTimeout bounds one upstream request so a hung network can't
	// keep the background refresh alive forever.
	FetchTimeout = 5 * time.Second
)
