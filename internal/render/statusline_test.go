package render_test

import (
	"strings"
	"testing"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/fetch"
	"github.com/PietroCoppola/newsfetch/internal/render"
)

// slNow is the fixed reference time for statusline renders. Fixture ages
// sit well inside a relativeAge bucket (2h is between the 1h and 24h
// boundaries) so the expected strings never depend on clock drift.
var slNow = time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)

func story(title, url string) fetch.Story {
	return fetch.Story{
		ID: "1", Title: title, URL: url, Source: "hackernews",
		Points: 100, Author: "alice", CreatedAt: slNow.Add(-2 * time.Hour),
		Tags: []string{},
	}
}

func TestStatusline(t *testing.T) {
	longTitle := story("A very long headline that will not fit at all", "https://example.com/a")
	anon := story("Go 1.26 released", "https://go.dev/blog/go1.26")
	anon.Author = ""

	cases := []struct {
		name     string
		story    fetch.Story
		maxWidth int
		want     string
	}{
		{
			name:     "linked underlined title with dim tail",
			story:    story("Go 1.26 released", "https://go.dev/blog/go1.26"),
			maxWidth: 80,
			want: "\x1b]8;;https://go.dev/blog/go1.26\x1b\\\x1b[4mGo 1.26 released\x1b[24m\x1b]8;;\x1b\\" +
				"\x1b[2m · go.dev · 2h ago · by alice\x1b[22m\n",
		},
		{
			name:     "no author drops the by segment",
			story:    anon,
			maxWidth: 80,
			want: "\x1b]8;;https://go.dev/blog/go1.26\x1b\\\x1b[4mGo 1.26 released\x1b[24m\x1b]8;;\x1b\\" +
				"\x1b[2m · go.dev · 2h ago\x1b[22m\n",
		},
		{
			name: "no URL renders plain, no underline, host unknown",
			// Underline signals clickability; a text-only story has nothing
			// to click, so the title renders bare. The tail still carries
			// the metadata, with Minimal's "unknown" host.
			story:    story("Ask HN: favourite pager?", ""),
			maxWidth: 80,
			want:     "Ask HN: favourite pager?\x1b[2m · unknown · 2h ago · by alice\x1b[22m\n",
		},
		{
			name:     "truncation shrinks the title and keeps the tail",
			story:    longTitle,
			maxWidth: 50,
			want: "\x1b]8;;https://example.com/a\x1b\\\x1b[4mA very long hea…\x1b[24m\x1b]8;;\x1b\\" +
				"\x1b[2m · example.com · 2h ago · by alice\x1b[22m\n",
		},
		{
			name: "pathologically narrow width drops the tail entirely",
			// The tail needs 34 columns; at 20 there is no room for both, and
			// a title stub beats metadata with no headline attached.
			story:    longTitle,
			maxWidth: 20,
			want:     "\x1b]8;;https://example.com/a\x1b\\\x1b[4mA very long headlin…\x1b[24m\x1b]8;;\x1b\\\n",
		},
		{
			name:     "zero maxWidth means no truncation",
			story:    story("A very long headline that will not fit at all", ""),
			maxWidth: 0,
			want: "A very long headline that will not fit at all" +
				"\x1b[2m · unknown · 2h ago · by alice\x1b[22m\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := render.Statusline(tc.story, slNow, tc.maxWidth)
			if got != tc.want {
				t.Errorf("Statusline() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestStatusline_DimClosesWithSGR22 pins the escape-hygiene contract: the
// tail must close dim with SGR 22, never SGR 0, or a full reset would clear
// the dim the surrounding status row wraps its own segments in.
func TestStatusline_DimClosesWithSGR22(t *testing.T) {
	got := render.Statusline(story("Go 1.26 released", "https://go.dev/blog/go1.26"), slNow, 80)
	if !strings.Contains(got, "\x1b[22m") {
		t.Errorf("tail does not close with SGR 22: %q", got)
	}
	if strings.Contains(got, "\x1b[0m") {
		t.Errorf("full reset (SGR 0) in output: %q", got)
	}
}

// CJK characters occupy two display columns each. Byte- or rune-count
// truncation would keep too many characters and overflow the status row —
// the whole reason runewidth is used.
func TestStatusline_CJKWidth(t *testing.T) {
	s := story("日本語のニュース見出しです", "") // 13 runes, 26 display columns
	got := render.Statusline(s, slNow, 10)
	got = strings.TrimSuffix(got, "\n")
	// Budget 10 columns: the tail cannot fit, so it is dropped and the whole
	// budget goes to the title. Ellipsis takes 1, so at most 4 CJK chars
	// (8 cols) + ellipsis = 9 columns fit; 5 chars + ellipsis = 11 overflows.
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected ellipsis suffix, got %q", got)
	}
	if want := "日本語のニ"; strings.Contains(got, want) {
		t.Errorf("kept 5 CJK chars (>10 columns): %q", got)
	}
}
