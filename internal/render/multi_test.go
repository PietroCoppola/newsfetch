package render_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/PietroCoppola/newsfetch/internal/fetch"
	"github.com/PietroCoppola/newsfetch/internal/render"
)

// fixture stories used across the multi-story render tests. Times are
// deterministic so output snapshots are stable.
func fixtureStories(now time.Time) []fetch.Story {
	return []fetch.Story{
		{
			Title:     "Hero story title",
			URL:       "https://example.com/hero",
			Source:    "hackernews",
			Author:    "alice",
			CreatedAt: now.Add(-2 * time.Hour),
		},
		{
			Title:     "Second story",
			URL:       "https://blog.rust-lang.org/post",
			Source:    "lobsters",
			CreatedAt: now.Add(-5 * time.Hour),
		},
		{
			Title:     "Third story",
			URL:       "https://example.org/news",
			Source:    "hackernews",
			CreatedAt: now.Add(-30 * time.Minute),
		},
	}
}

func TestMulti_SingleStoryDelegatesToBoxed(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	stories := fixtureStories(now)[:1]
	got := mustMulti(t, stories, now, 80, render.MultiOptions{Marker: render.TickerDot})
	want := render.Boxed(stories[0], now, 80)
	if got != want {
		t.Errorf("single-story Multi did not match Boxed\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestMulti_PlainTickerLines(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	stories := fixtureStories(now)
	got := mustMulti(t, stories, now, 80, render.MultiOptions{Marker: render.TickerDot})
	// Plain tickers: hero box, then two-space + bullet + body lines.
	if !strings.Contains(got, "  · Second story — blog.rust-lang.org (5h ago)\n") {
		t.Errorf("missing expected dot ticker line; got:\n%s", got)
	}
	if !strings.Contains(got, "  · Third story — example.org (30m ago)\n") {
		t.Errorf("missing expected second ticker line; got:\n%s", got)
	}
}

func TestMulti_BoxedTickersInsideBox(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	stories := fixtureStories(now)
	got := mustMulti(t, stories, now, 80, render.MultiOptions{Marker: render.TickerDot, Boxed: true})
	if !strings.Contains(got, "├") || !strings.Contains(got, "┤") {
		t.Errorf("expected divider with ├/┤ inside boxed render; got:\n%s", got)
	}
	if strings.Contains(got, "  · Second story") {
		t.Errorf("boxed ticker leaked plain-mode indent; got:\n%s", got)
	}
}

func TestMulti_BranchAddsSpine(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	stories := fixtureStories(now)
	plain := mustMulti(t, stories, now, 80, render.MultiOptions{Marker: render.TickerBranch})
	if !strings.Contains(plain, "╰─┬") {
		t.Errorf("plain branch render missing ╰─┬ spine anchor on hero bottom; got:\n%s", plain)
	}
	if !strings.Contains(plain, "  ├─ Second story") {
		t.Errorf("plain branch render missing ├─ marker; got:\n%s", plain)
	}
	if !strings.Contains(plain, "  └─ Third story") {
		t.Errorf("plain branch render missing └─ terminator on last entry; got:\n%s", plain)
	}

	boxed := mustMulti(t, stories, now, 80, render.MultiOptions{Marker: render.TickerBranch, Boxed: true})
	if !strings.Contains(boxed, "├─┬") {
		t.Errorf("boxed branch render missing ├─┬ spine anchor on divider; got:\n%s", boxed)
	}
}

func TestMulti_ArrowMarker(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	stories := fixtureStories(now)
	got := mustMulti(t, stories, now, 80, render.MultiOptions{Marker: render.TickerArrow})
	if !strings.Contains(got, "  ↳ Second story") {
		t.Errorf("arrow marker not applied; got:\n%s", got)
	}
}

func TestMulti_TickerTruncationKeepsHostAndAge(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	stories := fixtureStories(now)
	stories[1].Title = strings.Repeat("very long title ", 20)
	got := mustMulti(t, stories, now, 60, render.MultiOptions{Marker: render.TickerDot})
	// Even with a runaway title, host and age suffix must survive.
	if !strings.Contains(got, "blog.rust-lang.org") {
		t.Errorf("host stripped by truncation; got:\n%s", got)
	}
	if !strings.Contains(got, "(5h ago)") {
		t.Errorf("age stripped by truncation; got:\n%s", got)
	}
}

// mustMulti unwraps Multi for tests whose inputs are valid by
// construction.
func mustMulti(t *testing.T, stories []fetch.Story, now time.Time, width int, opts render.MultiOptions) string {
	t.Helper()
	got, err := render.Multi(stories, now, width, opts)
	if err != nil {
		t.Fatalf("Multi: %v", err)
	}
	return got
}

// clampedWidth mirrors render's unexported minWidth: every width in the
// narrow-width table clamps up to it, so it is the width the output must
// respect.
const clampedWidth = 10

// TestMulti_NarrowWidthDoesNotPanic covers widths below the structural
// minimum, where the multi renderers used to build negative-count
// strings.Repeat runs and panic. Multi clamps like Boxed instead — and the
// clamped width is a ceiling for the plain renderer's ticker rows too: a
// ticker row wider than its hero box is a ragged render, not a narrow one.
func TestMulti_NarrowWidthDoesNotPanic(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	stories := fixtureStories(now)
	for _, width := range []int{0, 1, 3, 9} {
		for _, boxed := range []bool{true, false} {
			t.Run(fmt.Sprintf("width=%d/boxed=%t", width, boxed), func(t *testing.T) {
				got, err := render.Multi(stories, now, width, render.MultiOptions{
					Marker: render.TickerBranch, Boxed: boxed,
				})
				if err != nil {
					t.Fatalf("Multi(width=%d, boxed=%t) error: %v", width, boxed, err)
				}
				if got == "" {
					t.Errorf("Multi(width=%d, boxed=%t) = empty, want a render", width, boxed)
				}
				if boxed {
					return
				}
				// Box-drawing and marker glyphs are single-column and the
				// fixtures are ASCII, so a rune count is a column count.
				for i, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
					if n := utf8.RuneCountInString(line); n > clampedWidth {
						t.Errorf("plain line %d is %d columns, want <= %d: %q",
							i, n, clampedWidth, line)
					}
				}
			})
		}
	}
}

func TestMulti_ErrorsOnEmpty(t *testing.T) {
	if _, err := render.Multi(nil, time.Now(), 80, render.MultiOptions{}); err == nil {
		t.Error("expected error on empty slice")
	}
}

func TestJSONMulti_EmitsArray(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	stories := fixtureStories(now)
	got := render.JSONMulti(stories, now)
	if !strings.HasPrefix(got, "[") {
		t.Errorf("JSONMulti must emit an array; got: %s", got)
	}
	if !strings.HasSuffix(strings.TrimRight(got, "\n"), "]") {
		t.Errorf("JSONMulti must emit a closed array; got: %s", got)
	}
	if !strings.Contains(got, `"title":"Hero story title"`) {
		t.Errorf("JSONMulti missing first story; got: %s", got)
	}
	if !strings.Contains(got, `"title":"Third story"`) {
		t.Errorf("JSONMulti missing last story; got: %s", got)
	}
}

// TestKnownTickerMarkers_ReturnsCopy pins the registry's immutability: a
// caller mutating the returned slice must not affect what later callers
// see (the validator and the wizards all consume this list).
func TestKnownTickerMarkers_ReturnsCopy(t *testing.T) {
	a := render.KnownTickerMarkers()
	a[0] = "mutated"
	if b := render.KnownTickerMarkers(); b[0] == "mutated" {
		t.Error("KnownTickerMarkers shares its backing array; want a fresh copy per call")
	}
}
