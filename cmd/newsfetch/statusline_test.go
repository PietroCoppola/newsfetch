package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/cache"
	"github.com/PietroCoppola/newsfetch/internal/config"
	"github.com/PietroCoppola/newsfetch/internal/fetch"
	"github.com/PietroCoppola/newsfetch/internal/history"
	"github.com/PietroCoppola/newsfetch/internal/lockfile"
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

// TestStatusline_PinnedRefreshSpawnsOutsideLock pins the invariant the
// pinned path's stale-flag indirection exists to preserve: spawnRefresh must
// run only after session.GetOrCreate has released sessions.lock, never from
// inside its create callback. Spawning inside the callback would extend the
// critical section that every concurrent statusline render in a busy
// session contends on — the callback looks like an ordinary function call,
// so nothing about its shape stops a future tidy-up from inlining the spawn
// back into it.
//
// It swaps spawnRefresh for a probe that tries to acquire sessions.lock
// itself with a zero timeout, using lockfile.Acquire directly rather than
// going through session.Read/Pin — a probe that goes through the package's
// own RMW helpers would itself need the lock and could never observe
// contention. Flock locks are scoped to the open file description, not the
// process, so the probe's independent os.OpenFile really does contend with
// session.update's still-open lock file if the spawn fires too early: while
// the real holder still has the lock open, the probe sees
// lockfile.ErrHeld; once session.update's defer has closed it, the probe's
// acquire succeeds.
//
// The probe records EVERY invocation rather than only the last one. A
// single shared variable overwritten by each call would pass a mutation
// that adds a second, in-lock spawnRefresh call beside the existing
// post-lock one: the in-lock call would record contention and the later
// post-lock call would silently overwrite it with success. Contention on
// any call is a failure, so calls and contended are counted across the
// whole invocation and asserted at the end.
func TestStatusline_PinnedRefreshSpawnsOutsideLock(t *testing.T) {
	isolateXDG(t)
	// A cache older than the TTL is what makes the pinned path set
	// needRefresh = true and call spawnRefresh at all.
	seedNewsPool(t, 1, time.Now().UTC().Add(-90*time.Minute))

	sPath, err := session.Path()
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(filepath.Dir(sPath), "sessions.lock")

	var calls, contended int
	var otherErr error
	restore := spawnRefresh
	spawnRefresh = func() {
		calls++
		lock, err := lockfile.Acquire(lockPath, 0)
		switch {
		case errors.Is(err, lockfile.ErrHeld):
			contended++
		case err != nil:
			otherErr = err
		default:
			lock.Close()
		}
	}
	t.Cleanup(func() { spawnRefresh = restore })

	out := runStatuslineArgs(t, 1, "--style=statusline", "--pin=prompt-1")
	if out == "" {
		t.Fatal("pinned render produced no output")
	}
	if calls == 0 {
		t.Fatal("spawnRefresh was never called; the seeded cache should have been stale enough to force it")
	}
	if contended > 0 {
		t.Fatalf("spawnRefresh ran while sessions.lock was still held on %d of %d call(s): the refresh is being spawned inside session.GetOrCreate's critical section", contended, calls)
	}
	if otherErr != nil {
		t.Fatalf("probe could not acquire sessions.lock for an unrelated reason: %v", otherErr)
	}
}

// testFeedURL is the single feed enableFollowing configures and
// seedFollowingPool attributes its stories to. They must agree: the cadence
// weight is looked up by Story.Feed, so a mismatch would silently drop the
// weight and the test would stop covering what it claims to.
const testFeedURL = "https://blog.example/feed.xml"

// writeUserConfig writes body as the config.toml inside the isolated XDG
// config root. Written as TOML rather than constructed in Go because the
// statusline reads its config off disk through config.Load, and the on-disk
// schema is what a user actually edits — including the clamps Validate
// applies to it.
func writeUserConfig(t *testing.T, configDir, body string) {
	t.Helper()
	path := filepath.Join(configDir, "newsfetch", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// enableFollowing turns the following pool on with one feed, leaving the news
// pool at its default aggregators.
func enableFollowing(t *testing.T, configDir string) {
	t.Helper()
	writeUserConfig(t, configDir, "pools = [\"following\", \"news\"]\n\n[[following.feeds]]\nurl = \""+testFeedURL+"\"\n")
}

// seedFollowingPool writes the following pool's cache with one story per
// title, fetched at fetchedAt. Ages are pinned at 90 minutes for the same
// reason seedNewsPool pins them: a stable "1h ago" bucket.
func seedFollowingPool(t *testing.T, fetchedAt time.Time, titles ...string) {
	t.Helper()
	now := time.Now().UTC()
	stories := make([]fetch.Story, len(titles))
	for i, title := range titles {
		stories[i] = fetch.Story{
			ID:        fmt.Sprintf("feed-%d", i),
			Title:     title,
			URL:       fmt.Sprintf("https://blog.example/post-%d", i),
			Source:    "following",
			Feed:      testFeedURL,
			Author:    "essayist",
			CreatedAt: now.Add(-90 * time.Minute),
			Tags:      []string{},
		}
	}
	path, err := cache.PoolPath("following")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeCache(path, stories, fetchedAt); err != nil {
		t.Fatal(err)
	}
}

// countSpawnRefresh swaps the detached-refresh seam for a counter so a test
// can assert the spawn without leaving a real background process behind.
// Restored via t.Cleanup, per the seam's contract.
func countSpawnRefresh(t *testing.T) *int {
	t.Helper()
	n := 0
	restore := spawnRefresh
	spawnRefresh = func() { n++ }
	t.Cleanup(func() { spawnRefresh = restore })
	return &n
}

// TestStatusline_FollowingBeatsNews pins design addendum item 1: precedence,
// never competition. The two rows differ only in how old the following cache
// is — a STALE following cache still wins over a fresh news one, because
// freshness never reorders the pools — and each row runs through BOTH
// statusline paths. If precedence landed in only one of them, a pinned turn
// and an unpinned turn could pick from different pools and the status row
// would flicker between them, which is the exact failure pinning exists to
// prevent.
func TestStatusline_FollowingBeatsNews(t *testing.T) {
	cases := []struct {
		name         string
		followingAge time.Duration
	}{
		{"fresh following cache", 1 * time.Minute},
		{"stale following cache", 90 * time.Minute},
	}
	paths := []struct {
		name string
		args []string
	}{
		{"unpinned", []string{"--style=statusline", "--pin="}},
		{"pinned", []string{"--style=statusline", "--pin=prompt-1"}},
	}
	for _, tc := range cases {
		for _, p := range paths {
			t.Run(tc.name+"/"+p.name, func(t *testing.T) {
				_, configDir := isolateXDG(t)
				enableFollowing(t, configDir)
				countSpawnRefresh(t)
				now := time.Now().UTC()
				seedFollowingPool(t, now.Add(-tc.followingAge), "The case for slow blogging")
				seedNewsPool(t, 1, now.Add(-1*time.Minute))

				out := runStatuslineArgs(t, 1, p.args...)
				if !strings.Contains(out, "The case for slow blogging") {
					t.Errorf("statusline = %q, want the following-pool story", out)
				}
				if strings.Contains(out, "Story A") {
					t.Errorf("statusline = %q, want no news-pool story", out)
				}
			})
		}
	}
}

// TestStatusline_ColdFollowingFallsBackToNews covers branch (a): a following
// pool with no cache file contributes nothing and must NOT fetch — the
// statusline never blocks on the network. It falls through to news and leaves
// a refresh behind for the next terminal open.
func TestStatusline_ColdFollowingFallsBackToNews(t *testing.T) {
	_, configDir := isolateXDG(t)
	enableFollowing(t, configDir)
	spawned := countSpawnRefresh(t)
	seedNewsPool(t, 1, time.Now().UTC().Add(-1*time.Minute))

	out := runStatuslineArgs(t, 1, "--style=statusline", "--pin=")
	if !strings.Contains(out, "Story A") {
		t.Errorf("statusline = %q, want the news-pool story", out)
	}
	if *spawned == 0 {
		t.Error("a cold following cache should have spawned a refresh")
	}
}

// TestStatusline_FullySeenFollowingFallsBackToNews covers branch (b): the
// following pool is filtered with the all-seen bypass OFF, so once its only
// story has been rendered the pool reports fully-seen and news gets the slot.
// With the bypass on, the second render would repeat the followed story and
// news would be unreachable forever.
func TestStatusline_FullySeenFollowingFallsBackToNews(t *testing.T) {
	_, configDir := isolateXDG(t)
	enableFollowing(t, configDir)
	countSpawnRefresh(t)
	now := time.Now().UTC()
	seedFollowingPool(t, now.Add(-1*time.Minute), "The case for slow blogging")
	seedNewsPool(t, 1, now.Add(-1*time.Minute))

	first := runStatuslineArgs(t, 1, "--style=statusline", "--pin=")
	if !strings.Contains(first, "The case for slow blogging") {
		t.Fatalf("first statusline = %q, want the followed story", first)
	}
	second := runStatuslineArgs(t, 1, "--style=statusline", "--pin=")
	if !strings.Contains(second, "Story A") {
		t.Errorf("second statusline = %q, want the news story once the only followed story is seen", second)
	}
}

// TestStatusline_NoFeedsIsByteIdenticalToV060 is a REGRESSION PIN, not a
// feature test. A user with no feeds configured must get exactly the v0.6.0
// status row, byte for byte: the OSC 8 link, the underlined title, the dim
// metadata tail closed with SGR 22, and nothing else. Any pool label, prefix,
// separator, or reordering that leaks out of the pool work into the status
// row fails here. --max-width is pinned so the assertion does not depend on
// terminal detection.
func TestStatusline_NoFeedsIsByteIdenticalToV060(t *testing.T) {
	isolateXDG(t) // no config file at all: pools defaults to ["news"]
	countSpawnRefresh(t)
	seedNewsPool(t, 1, time.Now().UTC().Add(-1*time.Minute))

	got := runStatuslineArgs(t, 1, "--style=statusline", "--pin=", "--max-width=80")
	want := "\x1b]8;;https://example.com/a\x1b\\" +
		"\x1b[4mStory A\x1b[24m" +
		"\x1b]8;;\x1b\\" +
		"\x1b[2m · example.com · 1h ago · by author-a\x1b[22m\n"
	if got != want {
		t.Errorf("statusline output changed for a no-feeds user:\n got = %q\nwant = %q", got, want)
	}
}

// TestStatusline_BothPoolsColdRendersNothing keeps today's shape when no
// active pool has anything cached: empty output beats a "no fresh news"
// banner in a status row, and a detached refresh is left behind.
func TestStatusline_BothPoolsColdRendersNothing(t *testing.T) {
	_, configDir := isolateXDG(t)
	enableFollowing(t, configDir)
	spawned := countSpawnRefresh(t)

	out := runStatuslineArgs(t, 1, "--style=statusline", "--pin=prompt-1")
	if out != "" {
		t.Errorf("output = %q, want empty (never block on the network)", out)
	}
	if *spawned == 0 {
		t.Error("both pools cold did not spawn a detached refresh")
	}
}

// TestStatusline_EmptyFollowingCacheIsNotStale pins ruling R-36 on the
// surface that pays for it most: the statusline runs on every prompt of every
// Claude Code turn, so a predicate that mistakes "empty" for "missing" spawns
// a detached process every prompt, forever, for any user whose feeds have
// gone quiet. Presence and freshness come from the cache file, not from the
// story count — an empty cache that is inside its TTL is present and fresh,
// contributes no story, and asks for nothing. The stale row is the control:
// the same empty cache past its TTL must still spawn, exactly once, or the
// pool could never refill.
func TestStatusline_EmptyFollowingCacheIsNotStale(t *testing.T) {
	cases := []struct {
		name         string
		followingAge time.Duration
		wantSpawns   int
	}{
		{"fresh empty following cache", 1 * time.Minute, 0},
		{"stale empty following cache", 90 * time.Minute, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, configDir := isolateXDG(t)
			enableFollowing(t, configDir)
			spawned := countSpawnRefresh(t)
			now := time.Now().UTC()
			// No titles: a cache file that read cleanly and holds zero
			// stories, which is what an all-304 refresh of a quiet feed
			// leaves behind.
			seedFollowingPool(t, now.Add(-tc.followingAge))
			seedNewsPool(t, 1, now.Add(-1*time.Minute))

			out := runStatuslineArgs(t, 1, "--style=statusline", "--pin=")
			if !strings.Contains(out, "Story A") {
				t.Errorf("statusline = %q, want the news story: an empty following pool contributes nothing", out)
			}
			if *spawned != tc.wantSpawns {
				t.Errorf("spawnRefresh called %d times, want %d", *spawned, tc.wantSpawns)
			}
		})
	}
}

// TestStatusline_FullySeenFollowingWithNoNewsRepeats pins ruling R-31:
// repeats beat silence, but only as a LAST resort. Here the following pool's
// only story is already seen and there is no news cache to fall through to,
// so every pool has come back empty while one of them still holds content.
// Re-showing that story is the right answer — a blank status row would be
// worse. This case isolates the following pool: with no news cache at all,
// the last-resort pass has exactly one pool to re-show, so it pins the
// re-show itself rather than the pool_order tie-break (which
// TestStatusline_AllSeenEverywhereRepeatsFirstPoolInPoolOrder covers).
func TestStatusline_FullySeenFollowingWithNoNewsRepeats(t *testing.T) {
	_, configDir := isolateXDG(t)
	enableFollowing(t, configDir)
	countSpawnRefresh(t)
	seedFollowingPool(t, time.Now().UTC().Add(-1*time.Minute), "The case for slow blogging")

	first := runStatuslineArgs(t, 1, "--style=statusline", "--pin=")
	if !strings.Contains(first, "The case for slow blogging") {
		t.Fatalf("first statusline = %q, want the followed story", first)
	}
	second := runStatuslineArgs(t, 1, "--style=statusline", "--pin=")
	if second != first {
		t.Errorf("second statusline = %q, want the seen followed story re-shown (%q)", second, first)
	}
}

// TestStatusline_AllSeenEverywhereRepeatsFirstPoolInPoolOrder is R-31's
// tie-break, and it is deliberately the same scenario and the same name as
// TestAssemblePools_TwoPassBypass's "all seen everywhere renders the first
// pool in pool_order" case. Both pools are present and every story in both
// has been rendered, so the last-resort pass runs and pool_order decides.
// The boxed render repeats from the first pool in pool_order; the status row
// must repeat from that same pool. If these two diverge, one user looking at
// one terminal sees the box re-show a followed post while the status row
// re-shows a news story — two surfaces disagreeing about the same state.
func TestStatusline_AllSeenEverywhereRepeatsFirstPoolInPoolOrder(t *testing.T) {
	_, configDir := isolateXDG(t)
	enableFollowing(t, configDir) // pool_order normalises to [following news]
	countSpawnRefresh(t)
	now := time.Now().UTC()
	seedFollowingPool(t, now.Add(-1*time.Minute), "The case for slow blogging")
	seedNewsPool(t, 1, now.Add(-1*time.Minute))

	// Render once per pool to drive both fully seen: precedence gives the
	// followed story first, then news once following has nothing new.
	first := runStatuslineArgs(t, 1, "--style=statusline", "--pin=")
	if !strings.Contains(first, "The case for slow blogging") {
		t.Fatalf("first statusline = %q, want the followed story", first)
	}
	second := runStatuslineArgs(t, 1, "--style=statusline", "--pin=")
	if !strings.Contains(second, "Story A") {
		t.Fatalf("second statusline = %q, want the news story", second)
	}

	third := runStatuslineArgs(t, 1, "--style=statusline", "--pin=")
	if third != first {
		t.Errorf("third statusline = %q, want the first pool in pool_order re-shown (%q)", third, first)
	}
	if strings.Contains(third, "Story A") {
		t.Errorf("third statusline = %q, want no news story: pool_order puts following first", third)
	}
}

// TestStatusline_NoFeedsFullySeenNewsRepeats is the v0.6.0 pin for the
// last-resort pass. With no feeds configured there is one pool, and once its
// only story is seen the FIRST pass yields nothing — the bypass is off for
// news now, exactly as it is for following. What keeps a no-feeds user's
// status row from going blank is the last-resort pass re-showing the seen
// news story. If that pass ever stops covering a single-pool config, this is
// the test that catches it, and the regression it catches is a status row
// that silently empties for every user who never configured a feed.
func TestStatusline_NoFeedsFullySeenNewsRepeats(t *testing.T) {
	isolateXDG(t) // no config file at all: pools defaults to ["news"]
	countSpawnRefresh(t)
	seedNewsPool(t, 1, time.Now().UTC().Add(-1*time.Minute))

	first := runStatuslineArgs(t, 1, "--style=statusline", "--pin=", "--max-width=80")
	if !strings.Contains(first, "Story A") {
		t.Fatalf("first statusline = %q, want the news story", first)
	}
	second := runStatuslineArgs(t, 1, "--style=statusline", "--pin=", "--max-width=80")
	if second != first {
		t.Errorf("second statusline = %q, want the seen news story re-shown (%q)", second, first)
	}
}

// TestStatusline_EmptyNewsAggregatorsIsNeverRead pins ruling R-35 on the
// statusline. The news pool is enabled but has no aggregators, so
// NewsActive() is false and the pool must not be read at all — not for a
// story, not for staleness. feed.json here is a ghost: stories cached before
// the user emptied the list. Gating on PoolEnabled instead of NewsActive
// serves those ghosts on every prompt while asking for a refresh that
// deliberately skips the pool.
//
// The second render is what proves the skip. The first is answered by
// precedence, which would hide the bug; the second falls out of a fully-seen
// following pool, where an active news pool WOULD take the slot.
func TestStatusline_EmptyNewsAggregatorsIsNeverRead(t *testing.T) {
	_, configDir := isolateXDG(t)
	// Validate keeps this shape: the following pool has content, so the
	// all-empty-pools clamp (R-9) does not fire and the empty aggregator
	// list survives Load (R-8).
	writeUserConfig(t, configDir, "pools = [\"following\", \"news\"]\n\n[news]\naggregators = []\n\n[[following.feeds]]\nurl = \""+testFeedURL+"\"\n")
	spawned := countSpawnRefresh(t)
	now := time.Now().UTC()
	seedFollowingPool(t, now.Add(-1*time.Minute), "The case for slow blogging")
	// Deliberately stale: an ACTIVE news pool would report itself stale
	// here and spawn. An inactive one is never read, so it cannot.
	seedNewsPool(t, 1, now.Add(-90*time.Minute))

	first := runStatuslineArgs(t, 1, "--style=statusline", "--pin=")
	if !strings.Contains(first, "The case for slow blogging") {
		t.Fatalf("first statusline = %q, want the followed story", first)
	}
	second := runStatuslineArgs(t, 1, "--style=statusline", "--pin=")
	if strings.Contains(second, "Story A") {
		t.Errorf("second statusline = %q, want no news story: the news pool has no aggregators", second)
	}
	if second != first {
		t.Errorf("second statusline = %q, want the seen followed story re-shown (%q)", second, first)
	}
	if *spawned != 0 {
		t.Errorf("spawnRefresh called %d times, want 0: the following cache is fresh, and a stale cache belonging to an inactive pool is never read", *spawned)
	}
}

// TestStatusline_LastResortHonoursReversedPoolOrder is the discriminating
// half of R-31's tie-break, and the reason it exists is that every other
// fixture in this file leaves pool_order unset — where it defaults to the
// compile-time order, which is the enabled-pools order too. Those two lists
// being equal makes the tie-break untestable: substituting the enabled set
// for the configured order in the last-resort loop passes every one of them.
//
// This config reverses them on purpose. pools stays [following news], so
// PRECEDENCE is unchanged and the followed story still takes the prime slot
// on the first render; pool_order is [news following], so once every story
// in both pools has been rendered the last-resort pass must re-show the NEWS
// story. Precedence is not pool_order, and pool_order is not the enabled
// list — this is the only test that can tell all three apart.
//
// The second half is the cross-surface pin the last-resort rule needs most,
// because that rule is implemented twice: once in assemblePools for the box,
// once in pickStatusline for the status row. Under one config and one
// seen-state they must name the SAME pool. If they ever drift, a user who
// sets pool_order = ["news", "following"] gets the box repeating one pool
// and the status row repeating the other, in the same terminal, in the same
// state — the exact divergence this task exists to prevent, and one that
// would otherwise ship green.
func TestStatusline_LastResortHonoursReversedPoolOrder(t *testing.T) {
	_, configDir := isolateXDG(t)
	// pools and pool_order deliberately disagree: orderPools keeps the
	// user's listed names first, so PoolOrder normalises to [news following]
	// while Pools stays [following news].
	writeUserConfig(t, configDir, "pools = [\"following\", \"news\"]\npool_order = [\"news\", \"following\"]\n\n[[following.feeds]]\nurl = \""+testFeedURL+"\"\n")
	countSpawnRefresh(t)
	now := time.Now().UTC()
	seedFollowingPool(t, now.Add(-1*time.Minute), "The case for slow blogging")
	seedNewsPool(t, 1, now.Add(-1*time.Minute))

	// Precedence ignores pool_order: following still goes first while it has
	// something unseen to offer. Two renders drive both pools fully seen.
	first := runStatuslineArgs(t, 1, "--style=statusline", "--pin=")
	if !strings.Contains(first, "The case for slow blogging") {
		t.Fatalf("first statusline = %q, want the followed story: precedence is not pool_order", first)
	}
	second := runStatuslineArgs(t, 1, "--style=statusline", "--pin=")
	if !strings.Contains(second, "Story A") {
		t.Fatalf("second statusline = %q, want the news story", second)
	}

	// Everything is seen, so the last-resort pass runs and pool_order — not
	// the enabled list, and not precedence — decides which pool repeats.
	third := runStatuslineArgs(t, 1, "--style=statusline", "--pin=")
	if !strings.Contains(third, "Story A") {
		t.Errorf("third statusline = %q, want the news story re-shown: pool_order puts news first", third)
	}
	if strings.Contains(third, "The case for slow blogging") {
		t.Errorf("third statusline = %q, want no followed story: it is first in pools but last in pool_order", third)
	}

	// Same config, same seen-state, the other surface. parseAndLoad is the
	// production load-and-validate path, so cfg here is exactly what the
	// statusline renders above were given.
	cfg, _, _, err := parseAndLoad(nil, io.Discard)
	if err != nil {
		t.Fatalf("parseAndLoad: %v", err)
	}
	if got, want := strings.Join(cfg.PoolOrder, ","), "news,following"; got != want {
		t.Fatalf("cfg.PoolOrder = %q, want %q: the fixture must actually reverse the order for this test to discriminate", got, want)
	}
	if got, want := strings.Join(cfg.Pools, ","), "following,news"; got != want {
		t.Fatalf("cfg.Pools = %q, want %q: pools and pool_order must disagree for this test to discriminate", got, want)
	}
	seen := loadSeen(cfg, now, io.Discard)
	pools, _, err := assemblePools(cfg, seen, now, rand.New(rand.NewSource(1)), io.Discard)
	if err != nil {
		t.Fatalf("assemblePools: %v", err)
	}
	var boxPool string
	poolsWithStories := 0
	for _, p := range pools {
		if len(p.Stories) > 0 {
			poolsWithStories++
			if boxPool != "" {
				t.Fatalf("assemblePools filled %d pools with stories, want exactly 1: the last resort spends the bypass on one pool only", poolsWithStories)
			}
			boxPool = p.Name
		}
	}
	if boxPool != "news" {
		t.Errorf("assemblePools re-showed pool %q, want \"news\"", boxPool)
	}
	// The two surfaces agreed on "news" only if BOTH assertions above held;
	// this restates the conclusion as one claim so a failure reads as the
	// cross-surface divergence it is rather than as two unrelated bugs.
	if boxPool == "news" && !strings.Contains(third, "Story A") {
		t.Error("the boxed render repeats the news pool but the status row does not: two surfaces, one user, one state")
	}
}
