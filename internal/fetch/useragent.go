package fetch

import (
	"fmt"

	"github.com/PietroCoppola/newsfetch/internal/defaults"
)

// userAgent returns the polite User-Agent string newsfetch sends on every
// outbound HTTP request. The +URL is reachable so a maintainer who notices
// our traffic can find the project. defaults.Version is interpolated so a
// version bump automatically updates the UA.
func userAgent() string {
	return fmt.Sprintf("newsfetch/%s (+https://github.com/PietroCoppola/newsfetch)", defaults.Version)
}
