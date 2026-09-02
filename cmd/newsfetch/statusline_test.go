package main

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/cache"
	"github.com/PietroCoppola/newsfetch/internal/config"
	"github.com/PietroCoppola/newsfetch/internal/fetch"
	"github.com/PietroCoppola/newsfetch/internal/history"
	"github.com/PietroCoppola/newsfetch/internal/session"
)

// seedStatuslineEnv isolates all user state via the existing isolateXDG
// helper (main_test.go) and writes a fresh cache with n distinct
// stories. Returns nothing; paths resolve via env.
func seedStatuslineEnv(t *testing.T, n int) {
	t.Helper()
	isolateXDG(t)
	stories := make([]fetch.Story, n)
	for i := range stories {
		stories[i] = fetch.Story{
			ID:     string(rune('a' + i)),
			Title:  "Story " + string(rune('A'+i)),
			URL:    "https://example.com/" + string(rune('a'+i)),
			Source: "hackernews",
			Points: 100 + i,
			Author: "author-" + string(rune('a'+i)),
			// 90 minutes sits mid-bucket, so relativeAge reads "1h ago"
			// for every render in a test run and byte-identical output
			// assertions cannot straddle a boundary.
			CreatedAt: time.Now().UTC().Add(-90 * time.Minute),
			Tags:      []string{},
		}
	}
	path, err := cache.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeCache(path, stories, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func runStatuslineArgs(t *testing.T, seed int64, args ...string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	rng := rand.New(rand.NewSource(seed))
	if err := runDefault(&out, &errOut, args, rng); err != nil {
		t.Fatalf("runDefault(%v) error: %v (stderr: %s)", args, err, errOut.String())
	}
	return out.String()
}

func TestStatusline_PinIsStableAcrossInvocations(t *testing.T) {
	seedStatuslineEnv(t, 8)
	first := runStatuslineArgs(t, 1, "--style=statusline", "--pin=prompt-1")
	// Different rng seed: only the pin can make this deterministic.
	second := runStatuslineArgs(t, 999, "--style=statusline", "--pin=prompt-1")
	if first == "" || first != second {
		t.Errorf("pinned renders differ:\n first = %q\nsecond = %q", first, second)
	}
	if !strings.Contains(first, "\x1b]8;;") {
		t.Errorf("output missing OSC 8 hyperlink: %q", first)
	}
}

func TestStatusline_NewPinKeySelectsFreshStory(t *testing.T) {
	seedStatuslineEnv(t, 8)
	first := runStatuslineArgs(t, 1, "--style=statusline", "--pin=prompt-1")
	second := runStatuslineArgs(t, 1, "--style=statusline", "--pin=prompt-2")
	// History dedup guarantees prompt-2 avoids prompt-1's story while the
	// pool has alternatives (8 stories, dedup window 6h).
	if first == second {
		t.Errorf("second pin repeated the first story: %q", first)
	}
	sPath, err := session.Path()
	if err != nil {
		t.Fatal(err)
	}
	f, err := session.Read(sPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != 2 {
		t.Errorf("session entries = %d, want 2", len(f.Entries))
	}
}

// TestStatusline_PinHitWritesNoHistory guards the invariant a pin exists
// to hold: one story, and one history entry, per user turn. A re-render on
// the same key must reuse the pinned entry rather than reselect and record
// a second story.
func TestStatusline_PinHitWritesNoHistory(t *testing.T) {
	seedStatuslineEnv(t, 8)
	first := runStatuslineArgs(t, 1, "--style=statusline", "--pin=X")
	if first == "" {
		t.Fatal("first pinned render produced no output")
	}
	// The pinned path renders from the stored entry, so the entry has to
	// carry author and created_at or the tail loses them.
	if !strings.Contains(first, "\x1b[2m · example.com · 1h ago · by author-") {
		t.Errorf("pinned render missing the metadata tail: %q", first)
	}
	if got := historyLen(t); got != 1 {
		t.Fatalf("history entries after first render = %d, want 1", got)
	}
	second := runStatuslineArgs(t, 999, "--style=statusline", "--pin=X")
	if second != first {
		t.Errorf("pin hit re-rendered a different story:\n first = %q\nsecond = %q", first, second)
	}
	if got := historyLen(t); got != 1 {
		t.Errorf("history entries after pin hit = %d, want 1 (a pin hit records nothing)", got)
	}
}

// historyLen reports how many entries seen.json holds under the isolated
// XDG state dir.
func historyLen(t *testing.T) int {
	t.Helper()
	path, err := history.Path()
	if err != nil {
		t.Fatal(err)
	}
	f, err := history.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	return len(f.Entries)
}

func TestStatusline_NoPinStillRenders(t *testing.T) {
	seedStatuslineEnv(t, 3)
	out := runStatuslineArgs(t, 1, "--style=statusline", "--pin=")
	if !strings.Contains(out, "Story ") {
		t.Errorf("unpinned render = %q, want a story line", out)
	}
	// The metadata tail is wired through from the selected story, dim and
	// closed with SGR 22.
	if !strings.Contains(out, "\x1b[2m · example.com · 1h ago · by author-") {
		t.Errorf("render missing the dim metadata tail: %q", out)
	}
	if !strings.HasSuffix(out, "\x1b[22m\n") {
		t.Errorf("render does not close dim with SGR 22: %q", out)
	}
}

func TestStatusline_CacheMissRendersNothing(t *testing.T) {
	isolateXDG(t)
	restore := spawnRefresh
	spawned := false
	spawnRefresh = func() { spawned = true }
	t.Cleanup(func() { spawnRefresh = restore })
	out := runStatuslineArgs(t, 1, "--style=statusline", "--pin=prompt-1")
	if out != "" {
		t.Errorf("cache-miss output = %q, want empty (never block on network)", out)
	}
	if !spawned {
		t.Error("cache miss did not spawn a detached refresh")
	}
}

func TestStatusline_MaxWidthTruncates(t *testing.T) {
	seedStatuslineEnv(t, 1)
	out := runStatuslineArgs(t, 1, "--style=statusline", "--pin=p", "--max-width=6")
	if !strings.Contains(out, "…") {
		t.Errorf("output %q not truncated at width 6", out)
	}
}

func TestReadPinKey(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"prompt_id preferred", `{"prompt_id":"p-1","session_id":"s-1"}`, "p-1"},
		{"session_id fallback", `{"session_id":"s-1"}`, "s-1"},
		{"empty payload", `{}`, ""},
		{"garbage", `not json at all`, ""},
		{"empty input", ``, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := readPinKey(strings.NewReader(tc.in)); got != tc.want {
				t.Errorf("readPinKey(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestResolvePinKey_FlagWinsOverStdin(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	w.WriteString(`{"prompt_id":"from-stdin"}`)
	w.Close()
	if got := resolvePinKey("from-flag", r); got != "from-flag" {
		t.Errorf("resolvePinKey = %q, want from-flag", got)
	}
}

func TestResolvePinKey_ReadsNonTTYStdin(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	w.WriteString(`{"prompt_id":"from-stdin"}`)
	w.Close()
	if got := resolvePinKey("", r); got != "from-stdin" {
		t.Errorf("resolvePinKey = %q, want from-stdin", got)
	}
}

// seedNewsPool writes the news pool's cache with n distinct stories, fetched
// at fetchedAt. The caller has already isolated XDG. Story ages are pinned at
// 90 minutes so relativeAge reads "1h ago" for every render in a test run and
// byte-identical output assertions cannot straddle a bucket boundary.
func seedNewsPool(t *testing.T, n int, fetchedAt time.Time) {
	t.Helper()
	stories := make([]fetch.Story, n)
	for i := range stories {
		stories[i] = fetch.Story{
			ID:        "news-" + string(rune('a'+i)),
			Title:     "Story " + string(rune('A'+i)),
			URL:       "https://example.com/" + string(rune('a'+i)),
			Source:    "hackernews",
			Points:    100 + i,
			Author:    "author-" + string(rune('a'+i)),
			CreatedAt: time.Now().UTC().Add(-90 * time.Minute),
			Tags:      []string{},
		}
	}
	path, err := cache.PoolPath("news")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeCache(path, stories, fetchedAt); err != nil {
		t.Fatal(err)
	}
}

// TestPickStatusline_NewsPool exercises the extracted helper directly: it
// returns the selected story, reports whether the cache it read has gone
// stale, and records exactly one history entry for what it handed back. The
// staleness flag is what the caller turns into a spawnRefresh AFTER the
// sessions lock is released, so it has to be a return value rather than a
// side effect.
func TestPickStatusline_NewsPool(t *testing.T) {
	cases := []struct {
		name        string
		cacheAge    time.Duration
		wantRefresh bool
	}{
		{"fresh cache needs no refresh", 1 * time.Minute, false},
		{"stale cache needs a refresh", 90 * time.Minute, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateXDG(t)
			now := time.Now().UTC()
			seedNewsPool(t, 1, now.Add(-tc.cacheAge))

			var errOut bytes.Buffer
			got, needRefresh, err := pickStatusline(
				config.Defaults(), map[string]struct{}{}, now,
				rand.New(rand.NewSource(1)), &errOut)
			if err != nil {
				t.Fatalf("pickStatusline: %v (stderr: %s)", err, errOut.String())
			}
			if got.Title != "Story A" {
				t.Errorf("selected title = %q, want %q", got.Title, "Story A")
			}
			if needRefresh != tc.wantRefresh {
				t.Errorf("needRefresh = %v, want %v", needRefresh, tc.wantRefresh)
			}
			if n := historyLen(t); n != 1 {
				t.Errorf("history entries = %d, want 1", n)
			}
		})
	}
}

// TestPickStatusline_ColdCacheIsErrNoCachedStories pins the sentinel the
// caller keys on: nothing cached is not a failure, it is the signal to print
// nothing and leave a refresh behind. Nothing may be recorded as rendered.
func TestPickStatusline_ColdCacheIsErrNoCachedStories(t *testing.T) {
	isolateXDG(t)
	_, _, err := pickStatusline(
		config.Defaults(), map[string]struct{}{}, time.Now().UTC(),
		rand.New(rand.NewSource(1)), io.Discard)
	if !errors.Is(err, errNoCachedStories) {
		t.Fatalf("pickStatusline error = %v, want errNoCachedStories", err)
	}
	if n := historyLen(t); n != 0 {
		t.Errorf("history entries = %d, want 0 (nothing was rendered)", n)
	}
}

// TestStatusline_PinnedAndUnpinnedPathsAgree is the guard the extraction
// exists for: both statusline paths must route through the one helper. With a
// single cached story the selection is deterministic, so any divergence
// between the two paths shows up as different bytes — which in a live session
// would be a status row flickering between a pinned turn and an unpinned one.
func TestStatusline_PinnedAndUnpinnedPathsAgree(t *testing.T) {
	isolateXDG(t)
	seedNewsPool(t, 1, time.Now().UTC().Add(-1*time.Minute))
	unpinned := runStatuslineArgs(t, 1, "--style=statusline", "--pin=")
	if unpinned == "" {
		t.Fatal("unpinned render produced no output")
	}

	isolateXDG(t) // fresh state: the first render recorded history
	seedNewsPool(t, 1, time.Now().UTC().Add(-1*time.Minute))
	pinned := runStatuslineArgs(t, 1, "--style=statusline", "--pin=prompt-1")
	if pinned != unpinned {
		t.Errorf("pinned and unpinned paths disagree:\n  pinned = %q\nunpinned = %q", pinned, unpinned)
	}
}
