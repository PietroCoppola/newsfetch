package render

import (
	"testing"
	"time"
	"unicode/utf8"

	"github.com/PietroCoppola/newsfetch/internal/fetch"
)

// TestTopBorder_EmptyLabel pins the extraction against the literal borders
// the three former call sites built inline. These are the exact top lines
// the package's byte-exact goldens already assert, so a drift here is a
// drift in every boxed render.
func TestTopBorder_EmptyLabel(t *testing.T) {
	cases := []struct {
		name  string
		width int
		want  string
	}{
		{"structural minimum", 10, "╭────────╮"},
		{"narrow panel", 34, "╭────────────────────────────────╮"},
		{"default panel", 50, "╭────────────────────────────────────────────────╮"},
		{"wide panel", 80, "╭──────────────────────────────────────────────────────────────────────────────╮"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := topBorder(tc.width, "")
			if got != tc.want {
				t.Errorf("topBorder(%d, \"\") = %q, want %q", tc.width, got, tc.want)
			}
			if n := utf8.RuneCountInString(got); n != tc.width {
				t.Errorf("topBorder(%d, \"\") is %d columns, want %d", tc.width, n, tc.width)
			}
		})
	}
}

// TestTopBorder_RoutesEveryBoxTop checks that all three box builders emit
// the shared border rather than a private copy of it: a copy left behind
// would silently keep its old shape when the label branch lands.
func TestTopBorder_RoutesEveryBoxTop(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	stories := []fetch.Story{
		{Title: "Hero story title", URL: "https://example.com/hero", CreatedAt: now.Add(-2 * time.Hour)},
		{Title: "Second story", URL: "https://example.org/two", CreatedAt: now.Add(-5 * time.Hour)},
	}
	const width = 50
	want := topBorder(width, "")
	boxedMulti, err := Multi(stories, now, width, MultiOptions{Marker: TickerDot, Boxed: true})
	if err != nil {
		t.Fatalf("Multi boxed: %v", err)
	}
	spineMulti, err := Multi(stories, now, width, MultiOptions{Marker: TickerBranch})
	if err != nil {
		t.Fatalf("Multi branch: %v", err)
	}
	cases := []struct {
		name string
		got  string
	}{
		{"single box", boxedLabelled(stories[0], now, width, "")},
		{"boxed multi", boxedMulti},
		{"plain multi hero with spine", spineMulti},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first, _, ok := cutLine(tc.got)
			if !ok {
				t.Fatalf("%s produced no line: %q", tc.name, tc.got)
			}
			if first != want {
				t.Errorf("%s top line = %q, want %q", tc.name, first, want)
			}
		})
	}
}

// cutLine splits off the first line of s, without its newline.
func cutLine(s string) (line, rest string, ok bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i], s[i+1:], true
		}
	}
	return "", s, false
}

// TestTopBorder_Labelled pins ruling R-19: a label is written into the
// border, truncated when it does not fit, and dropped entirely when even a
// truncated label cannot leave one trailing dash. The box never widens —
// stacked pool boxes have to align, and a widened box also feeds a negative
// strings.Repeat count, which is how this class of bug shipped once before.
func TestTopBorder_Labelled(t *testing.T) {
	cases := []struct {
		name  string
		width int
		label string
		want  string
	}{
		{"label fits with room to spare", 50, "Following", "╭─ Following ────────────────────────────────────╮"},
		{"short label fits", 50, "News", "╭─ News ─────────────────────────────────────────╮"},
		{"label fits with exactly one trailing dash", 15, "Following", "╭─ Following ─╮"},
		{"label truncated to fit", 14, "Following", "╭─ Followi… ─╮"},
		{"label truncated to the ellipsis alone", 7, "Following", "╭─ … ─╮"},
		{"too narrow for any label falls back to plain", 6, "Following", "╭────╮"},
		{"pathologically narrow falls back to plain", 3, "News", "╭─╮"},
		{"empty label is the plain border", 50, "", "╭────────────────────────────────────────────────╮"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := topBorder(tc.width, tc.label)
			if got != tc.want {
				t.Errorf("topBorder(%d, %q) = %q, want %q", tc.width, tc.label, got, tc.want)
			}
			if n := utf8.RuneCountInString(got); n != tc.width {
				t.Errorf("topBorder(%d, %q) is %d columns, want exactly %d — the label must never resize the box",
					tc.width, tc.label, n, tc.width)
			}
		})
	}
}
