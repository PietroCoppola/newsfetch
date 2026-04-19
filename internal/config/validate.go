package config

import (
	"fmt"
	"io"
	"time"
)

// FieldSources tells Validate where each validatable field came from so
// warnings can name the source. Zero value for a field means "from defaults"
// and produces no warning (defaults are valid by construction).
type FieldSources struct {
	// Style is "" (defaults), "config", or "flag".
	Style string
}

const minCacheTTL = 5 * time.Minute

// Validate inspects the merged Config, clamps out-of-range fields, and emits
// at most one warning line to w naming the offending field and its source.
// Returns the corrected Config. Callers pass os.Stderr in production and a
// bytes.Buffer in tests. Not called when Load returned a parse error —
// defaults are valid by construction.
//
// Precedence of warnings when multiple fields are bad (first wins):
//  1. unknown style
//  2. cache_ttl_minutes below minimum
//  3. min_points below 0
func Validate(c Config, src FieldSources, w io.Writer) Config {
	switch c.Style {
	case "boxed", "minimal", "json":
	default:
		bad := c.Style
		c.Style = Defaults().Style
		fmt.Fprintf(w, "newsfetch: unknown style %q (%s), using %q\n", bad, sourceLabel(src.Style), c.Style)
		// Fall through to silent corrections.
		c = silentlyCorrect(c)
		return c
	}
	if c.CacheTTL < minCacheTTL {
		badMins := int(c.CacheTTL / time.Minute)
		c.CacheTTL = minCacheTTL
		fmt.Fprintf(w, "newsfetch: cache_ttl_minutes=%d below minimum 5, using 5\n", badMins)
		c = silentlyCorrect(c)
		return c
	}
	if c.MinPoints < 0 {
		bad := c.MinPoints
		c.MinPoints = 0
		fmt.Fprintf(w, "newsfetch: min_points=%d below 0, using 0\n", bad)
		return c
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
	return c
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
