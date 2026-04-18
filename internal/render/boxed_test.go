package render_test

import (
	"testing"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/fetch"
	"github.com/PietroCoppola/newsfetch/internal/render"
)

func TestBoxed_Basic(t *testing.T) {
	now := time.Date(2026, 4, 18, 10, 0, 0, 0, time.UTC)
	story := fetch.Story{
		ID:        "hn-1",
		Title:     "React 21 drops with native signals",
		URL:       "https://reactjs.org/blog/2026/react-21",
		Source:    "hackernews",
		Points:    420,
		Author:    "alice",
		CreatedAt: now.Add(-2 * time.Hour),
	}

	got := render.Boxed(story, now, 50)
	want := "" +
		"╭────────────────────────────────────────────────╮\n" +
		"│ React 21 drops with native signals             │\n" +
		"│ reactjs.org · 2h ago · by alice                │\n" +
		"╰────────────────────────────────────────────────╯\n"
	if got != want {
		t.Errorf("Boxed mismatch\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

func TestBoxed_LongTitleTruncatesWithEllipsis(t *testing.T) {
	now := time.Date(2026, 4, 18, 10, 0, 0, 0, time.UTC)
	story := fetch.Story{
		Title:     "A really long title that definitely will not fit in thirty characters of content",
		URL:       "https://example.com/x",
		Author:    "alice",
		CreatedAt: now.Add(-2 * time.Hour),
	}

	got := render.Boxed(story, now, 34)
	// contentWidth = 30; title becomes 29 runes + "…"
	want := "" +
		"╭────────────────────────────────╮\n" +
		"│ A really long title that defi… │\n" +
		"│ example.com · 2h ago · by ali… │\n" +
		"╰────────────────────────────────╯\n"
	if got != want {
		t.Errorf("Boxed mismatch\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

func TestBoxed_OmitsAuthorSegmentWhenEmpty(t *testing.T) {
	now := time.Date(2026, 4, 18, 10, 0, 0, 0, time.UTC)
	story := fetch.Story{
		Title:     "Short title",
		URL:       "https://example.com/x",
		Author:    "",
		CreatedAt: now.Add(-5 * time.Minute),
	}

	got := render.Boxed(story, now, 40)
	want := "" +
		"╭──────────────────────────────────────╮\n" +
		"│ Short title                          │\n" +
		"│ example.com · 5m ago                 │\n" +
		"╰──────────────────────────────────────╯\n"
	if got != want {
		t.Errorf("Boxed mismatch\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

func TestBoxed_StripsWWWFromHostname(t *testing.T) {
	now := time.Date(2026, 4, 18, 10, 0, 0, 0, time.UTC)
	story := fetch.Story{
		Title:     "Title",
		URL:       "https://www.example.com/path",
		Author:    "alice",
		CreatedAt: now.Add(-1 * time.Hour),
	}
	got := render.Boxed(story, now, 50)
	// meta should start with "example.com" (www stripped)
	if !containsLine(got, "│ example.com · 1h ago · by alice") {
		t.Errorf("Boxed did not strip www prefix\n%s", got)
	}
}

func TestRelativeAgeViaBoxed(t *testing.T) {
	now := time.Date(2026, 4, 18, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		offset  time.Duration
		wantAge string
	}{
		{"under one minute", 10 * time.Second, "just now"},
		{"exactly one minute", time.Minute, "1m ago"},
		{"multiple minutes", 42 * time.Minute, "42m ago"},
		{"exactly one hour", time.Hour, "1h ago"},
		{"multiple hours", 5 * time.Hour, "5h ago"},
		{"exactly one day", 24 * time.Hour, "1d ago"},
		{"multiple days", 3 * 24 * time.Hour, "3d ago"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			story := fetch.Story{
				Title:     "x",
				URL:       "https://example.com",
				Author:    "",
				CreatedAt: now.Add(-tc.offset),
			}
			got := render.Boxed(story, now, 60)
			want := "│ example.com · " + tc.wantAge
			if !containsLine(got, want) {
				t.Errorf("missing %q in output:\n%s", want, got)
			}
		})
	}
}

func TestHostname_Unknown(t *testing.T) {
	now := time.Date(2026, 4, 18, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		url  string
	}{
		{"empty", ""},
		{"no scheme no host", "not a url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			story := fetch.Story{
				Title:     "x",
				URL:       tc.url,
				CreatedAt: now.Add(-time.Hour),
			}
			got := render.Boxed(story, now, 50)
			if !containsLine(got, "│ unknown · 1h ago") {
				t.Errorf("expected 'unknown' hostname fallback:\n%s", got)
			}
		})
	}
}

func TestFallback_ReturnsMessageWithNewline(t *testing.T) {
	got := render.Fallback("no fresh news — check your connection")
	want := "no fresh news — check your connection\n"
	if got != want {
		t.Errorf("Fallback = %q, want %q", got, want)
	}
}

// containsLine reports whether any line in out starts with the given prefix.
// Helpful for assertions that only care about a specific row of the panel.
func containsLine(out, prefix string) bool {
	for _, line := range splitLines(out) {
		if len(line) >= len(prefix) && line[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
