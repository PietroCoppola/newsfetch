package render_test

import (
	"strings"
	"testing"
	"time"

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
	got := render.Multi(stories, now, 80, render.MultiOptions{Marker: render.TickerDot})
	want := render.Boxed(stories[0], now, 80)
	if got != want {
		t.Errorf("single-story Multi did not match Boxed\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestMulti_PlainTickerLines(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	stories := fixtureStories(now)
	got := render.Multi(stories, now, 80, render.MultiOptions{Marker: render.TickerDot})
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
	got := render.Multi(stories, now, 80, render.MultiOptions{Marker: render.TickerDot, Boxed: true})
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
	plain := render.Multi(stories, now, 80, render.MultiOptions{Marker: render.TickerBranch})
	if !strings.Contains(plain, "╰─┬") {
		t.Errorf("plain branch render missing ╰─┬ spine anchor on hero bottom; got:\n%s", plain)
	}
	if !strings.Contains(plain, "  ├─ Second story") {
		t.Errorf("plain branch render missing ├─ marker; got:\n%s", plain)
	}
	if !strings.Contains(plain, "  └─ Third story") {
		t.Errorf("plain branch render missing └─ terminator on last entry; got:\n%s", plain)
	}

	boxed := render.Multi(stories, now, 80, render.MultiOptions{Marker: render.TickerBranch, Boxed: true})
	if !strings.Contains(boxed, "├─┬") {
		t.Errorf("boxed branch render missing ├─┬ spine anchor on divider; got:\n%s", boxed)
	}
}

func TestMulti_ArrowMarker(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	stories := fixtureStories(now)
	got := render.Multi(stories, now, 80, render.MultiOptions{Marker: render.TickerArrow})
	if !strings.Contains(got, "  ↳ Second story") {
		t.Errorf("arrow marker not applied; got:\n%s", got)
	}
}

func TestMulti_TickerTruncationKeepsHostAndAge(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	stories := fixtureStories(now)
	stories[1].Title = strings.Repeat("very long title ", 20)
	got := render.Multi(stories, now, 60, render.MultiOptions{Marker: render.TickerDot})
	// Even with a runaway title, host and age suffix must survive.
	if !strings.Contains(got, "blog.rust-lang.org") {
		t.Errorf("host stripped by truncation; got:\n%s", got)
	}
	if !strings.Contains(got, "(5h ago)") {
		t.Errorf("age stripped by truncation; got:\n%s", got)
	}
}

func TestMulti_PanicsOnEmpty(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic on empty slice")
		}
	}()
	render.Multi(nil, time.Now(), 80, render.MultiOptions{})
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
