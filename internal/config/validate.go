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
//  2. sources empty or all-unknown
//  3. cache_ttl_minutes below minimum
//  4. min_points below 0
//  5. count out of [1, MaxCount]
//  6. unknown ticker_marker
//  7. dedup_ttl_hours negative
//
// Every early return must route through silentlyCorrect so fields below the
// first warning still get clamped. If a new field is added to the cascade,
// extend silentlyCorrect to cover it.
func Validate(c Config, src FieldSources, w io.Writer) Config {
	minMins := int(minCacheTTL / time.Minute)
	switch c.Style {
	case "boxed", "minimal", "json":
	default:
		bad := c.Style
		c.Style = Defaults().Style
		fmt.Fprintf(w, "newsfetch: unknown style %q (%s), using %q\n", bad, sourceLabel(src.Style, "style"), c.Style)
		return silentlyCorrect(c)
	}
	valid, dropped := splitSources(c.Sources)
	if len(valid) == 0 {
		// Either user wrote sources=[], or every name was unknown. Either
		// way, running with no sources means the renderer would always
		// hit the offline fallback — fail loud and reset to defaults so
		// the next invocation actually shows news.
		c.Sources = Defaults().Sources
		if len(dropped) > 0 {
			fmt.Fprintf(w, "newsfetch: sources contained no recognised names (dropped: %v); using %v\n", dropped, c.Sources)
		} else {
			fmt.Fprintf(w, "newsfetch: sources is empty; using %v\n", c.Sources)
		}
		return silentlyCorrect(c)
	}
	if len(dropped) > 0 {
		c.Sources = valid
		fmt.Fprintf(w, "newsfetch: unknown source name(s) %v dropped; using %v\n", dropped, valid)
		return silentlyCorrect(c)
	}
	c.Sources = valid
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
	return c
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
	for _, m := range render.KnownTickerMarkers {
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
	if valid, _ := splitSources(c.Sources); len(valid) == 0 {
		c.Sources = Defaults().Sources
	} else {
		c.Sources = valid
	}
	c.Count = clampCount(c.Count)
	if !knownTickerMarker(c.TickerMarker) {
		c.TickerMarker = Defaults().TickerMarker
	}
	if c.DedupWindow < 0 {
		c.DedupWindow = 0
	}
	return c
}

// splitSources partitions names into recognised vs unknown, preserving order.
// Recognition uses fetch.KnownSourceNames as the single source of truth.
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
	for _, k := range fetch.KnownSourceNames {
		if k == name {
			return true
		}
	}
	return false
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
