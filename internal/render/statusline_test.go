package render_test

import (
	"strings"
	"testing"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/fetch"
	"github.com/PietroCoppola/newsfetch/internal/render"
)

func story(title, url string) fetch.Story {
	return fetch.Story{
		ID: "1", Title: title, URL: url, Source: "hackernews",
		Points: 100, Author: "alice", CreatedAt: time.Now(), Tags: []string{},
	}
}

func TestStatusline(t *testing.T) {
	cases := []struct {
		name     string
		story    fetch.Story
		maxWidth int
		want     string
	}{
		{
			name:     "linked and underlined",
			story:    story("Go 1.26 released", "https://go.dev/blog/go1.26"),
			maxWidth: 80,
			want:     "\x1b]8;;https://go.dev/blog/go1.26\x1b\\\x1b[4mGo 1.26 released\x1b[24m\x1b]8;;\x1b\\\n",
		},
		{
			name: "no URL renders plain, no underline",
			// Underline signals clickability; a text-only story has nothing
			// to click, so it renders bare.
			story:    story("Ask HN: favourite pager?", ""),
			maxWidth: 80,
			want:     "Ask HN: favourite pager?\n",
		},
		{
			name:     "truncates to display columns with ellipsis",
			story:    story("A very long headline that will not fit at all", "https://example.com/a"),
			maxWidth: 20,
			want:     "\x1b]8;;https://example.com/a\x1b\\\x1b[4mA very long headlin…\x1b[24m\x1b]8;;\x1b\\\n",
		},
		{
			name:     "zero maxWidth means no truncation",
			story:    story("A very long headline that will not fit at all", ""),
			maxWidth: 0,
			want:     "A very long headline that will not fit at all\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := render.Statusline(tc.story, tc.maxWidth)
			if got != tc.want {
				t.Errorf("Statusline() = %q, want %q", got, tc.want)
			}
		})
	}
}

// CJK characters occupy two display columns each. Byte- or rune-count
// truncation would keep too many characters and overflow the status row —
// the whole reason runewidth is used.
func TestStatusline_CJKWidth(t *testing.T) {
	s := story("日本語のニュース見出しです", "") // 13 runes, 26 display columns
	got := render.Statusline(s, 10)
	got = strings.TrimSuffix(got, "\n")
	// Budget 10 columns: ellipsis takes 1, so at most 4 CJK chars (8 cols)
	// + ellipsis = 9 columns fit; 5 chars + ellipsis = 11 would overflow.
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected ellipsis suffix, got %q", got)
	}
	if want := "日本語のニ"; strings.Contains(got, want) {
		t.Errorf("kept 5 CJK chars (>10 columns): %q", got)
	}
}
