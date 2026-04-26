package config

import (
	"fmt"
	"io"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/fetch"
)

// FieldSources tells Validate where each validatable field came from so
// warnings can name the source. Zero value for a field means "from defaults"
// and produces no warning (defaults are valid by construction).
type FieldSources struct {
	// Style is "" (defaults), "config", or "flag".
	Style string
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
		fmt.Fprintf(w, "newsfetch: unknown style %q (%s), using %q\n", bad, sourceLabel(src.Style), c.Style)
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
	return c
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

func sourceLabel(src string) string {
	switch src {
	case "flag":
		return "from --style"
	case "config":
		return "from config"
	default:
		return "from defaults"
	}
}
