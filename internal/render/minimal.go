package render

import (
	"strings"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/fetch"
)

// Minimal renders s as a single line suitable for shells that prefer a
// tight prompt decoration. A leading space matches spec §10's mockup.
// No emoji, no box drawing, no width clamp — the terminal wraps long
// titles naturally.
func Minimal(s fetch.Story, now time.Time) string {
	parts := []string{s.Title, hostname(s.URL), relativeAge(s.CreatedAt, now)}
	if s.Author != "" {
		parts = append(parts, "by "+s.Author)
	}
	return " " + strings.Join(parts, " · ") + "\n"
}
