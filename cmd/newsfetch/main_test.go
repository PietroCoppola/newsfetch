package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/cache"
	"github.com/PietroCoppola/newsfetch/internal/config"
	"github.com/PietroCoppola/newsfetch/internal/defaults"
	"github.com/PietroCoppola/newsfetch/internal/fetch"
	"github.com/PietroCoppola/newsfetch/internal/lockfile"
	"github.com/PietroCoppola/newsfetch/internal/onboard"
	"github.com/PietroCoppola/newsfetch/internal/refreshlog"
	"github.com/PietroCoppola/newsfetch/internal/render"
)

// isolateXDG points every XDG root (and HOME, which the XDG fallbacks
// resolve through) at fresh temp dirs so a test can never read or write
// the real user's cache, config, or state. Every test that invokes
// runDefault MUST call this first: a successful render appends to
// seen.json via history.Path, whose fallback is the real ~/.local/state,
// and reads the real config and history when those envs leak through.
func isolateXDG(t *testing.T) (cacheDir, configDir string) {
	t.Helper()
	cacheDir = t.TempDir()
	configDir = t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	return cacheDir, configDir
}

// TestRunDefault_RendersFromFreshCache seeds a cache file under
// XDG_CACHE_HOME and verifies runDefault prints a boxed story without going
// near the network. The cold-start fetch-on-miss path is covered by
// TestRunDefault_ColdStart_FetchesAndCaches below; the stochastic
// topic-boost behaviour is covered by the WarmCache win-rate tests.
func TestRunDefault_RendersFromFreshCache(t *testing.T) {
	isolateXDG(t)

	path, err := cache.Path()
	if err != nil {
		t.Fatalf("cache.Path: %v", err)
	}
	now := time.Now().UTC()
	story := fetch.Story{
		ID:        "hn-1",
		Title:     "A seeded story",
		URL:       "https://example.com/x",
		Source:    "hackernews",
		Points:    100,
		Author:    "alice",
		CreatedAt: now.Add(-2 * time.Hour),
		Tags:      []string{},
	}
	if err := cache.Write(path, &cache.File{
		Version:         cache.SchemaVersion,
		CachedByVersion: defaults.Version,
		FetchedAt:       now.Add(-5 * time.Minute), // fresh
		Stories:         []fetch.Story{story},
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	var buf bytes.Buffer
	if err := runDefault(&buf, io.Discard, nil, rand.New(rand.NewSource(1))); err != nil {
		t.Fatalf("runDefault: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"A seeded story", "example.com", "2h ago", "by alice"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output:\n%s", want, out)
		}
	}
}

// algoliaStub serves a fixed pool of stories where "React 21" has raw
// dominance (400 points, 2h old) and "Rust 1.87" is competitive but not
// dominant (300 points, 3h old). With no topics, React wins the pool
// most of the time. With topics=["rust"], Rust gets a 2x multiplier
// and becomes the strongest candidate.
//
// Timestamps are computed relative to the moment the handler is invoked so
// that the age-based ranking ratios remain stable regardless of when the
// test suite runs.
func algoliaStub() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reactTime := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
		rustTime := time.Now().UTC().Add(-3 * time.Hour).Format(time.RFC3339)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"hits": [
				{"objectID":"1","title":"React 21 drops with native signals","url":"https://reactjs.org/","points":400,"author":"alice","created_at":%q},
				{"objectID":"2","title":"Rust 1.87 stabilizes async closures","url":"https://rust-lang.org/","points":300,"author":"bob","created_at":%q}
			]
		}`, reactTime, rustTime)
	}))
}

func TestParseAndLoad_PinAndMaxWidthFlags(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no config file → defaults
	var buf bytes.Buffer
	cfg, cli, exit, err := parseAndLoad(
		[]string{"--style=statusline", "--pin=prompt-abc", "--max-width=60"}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if exit != exitRun {
		t.Fatalf("exit = %v, want exitRun", exit)
	}
	if cfg.Style != "statusline" {
		t.Errorf("Style = %q, want statusline", cfg.Style)
	}
	if cli.pin != "prompt-abc" {
		t.Errorf("pin = %q, want prompt-abc", cli.pin)
	}
	if cli.maxWidth != 60 {
		t.Errorf("maxWidth = %d, want 60", cli.maxWidth)
	}
}

func TestParseAndLoad_NegativeMaxWidthMeansAuto(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var buf bytes.Buffer
	_, cli, _, err := parseAndLoad([]string{"--max-width=-5"}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if cli.maxWidth != 0 {
		t.Errorf("maxWidth = %d, want 0 (auto)", cli.maxWidth)
	}
}

// TestRunRefresh_SkipsWhenAnotherRefreshHoldsLock covers the single-flight
// guard: a cold statusline render and a multi-tab terminal restore can each
// spawn --__refresh, and only the first should reach the network. The
// stand-in factory both fails the test and refuses to build a source, so a
// regression cannot leak a real HN request out of the test suite.
func TestRunRefresh_SkipsWhenAnotherRefreshHoldsLock(t *testing.T) {
	isolateXDG(t)
	path, err := cache.Path()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	held, err := lockfile.Acquire(filepath.Join(dir, "refresh.lock"), time.Second)
	if err != nil {
		t.Fatalf("test could not take the refresh lock: %v", err)
	}
	t.Cleanup(func() { held.Close() })

	original := newSource
	newSource = func(name string) (fetch.Source, error) {
		t.Errorf("runRefresh built source %q while another refresh held the lock", name)
		return nil, fmt.Errorf("source %q must not be built", name)
	}
	t.Cleanup(func() { newSource = original })

	if err := runRefresh(); err != nil {
		t.Errorf("runRefresh() = %v, want nil (another refresh is already in flight)", err)
	}
}

// TestRunRefresh_UnopenableLockIsAFailureNotASkip separates contention
// from fault: the single-flight guard must stay quiet only when another
// refresh holds the lock. A lock that cannot be opened at all — here an
// unwritable cache dir — has to propagate, or refresh exits 0 forever and
// nothing ever reaches refreshlog to say why the cache went stale.
func TestRunRefresh_UnopenableLockIsAFailureNotASkip(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions do not deny access")
	}
	isolateXDG(t)
	path, err := cache.Path()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// r-x: the dir exists (so MkdirAll succeeds) but refresh.lock cannot be
	// created in it.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	// Belt and braces: on a host where chmod does not deny (a
	// permission-ignoring mount, CAP_DAC_OVERRIDE), the lock would open and
	// the refresh would run — the suite must never reach real HN.
	original := newSource
	newSource = func(name string) (fetch.Source, error) {
		t.Error("source constructed — lock failure should abort before any fetch")
		return nil, fmt.Errorf("source %q must not be built", name)
	}
	t.Cleanup(func() { newSource = original })

	err = runRefresh()
	if err == nil {
		t.Fatal("runRefresh() = nil for an unopenable lock, want an error reaching refreshlog")
	}
	if !strings.Contains(err.Error(), "refresh lock") {
		t.Errorf("error = %v, want it wrapped as a refresh lock failure", err)
	}
}

func swapHNSource(t *testing.T, url string) {
	t.Helper()
	original := newSource
	newSource = func(name string) (fetch.Source, error) {
		switch name {
		case "hackernews":
			return &fetch.HackerNews{BaseURL: url}, nil
		default:
			return original(name)
		}
	}
	t.Cleanup(func() { newSource = original })
}

// TestRunDefault_ColdStart_FetchesAndCaches covers the cold-start wiring:
// no cache file, no config, runDefault calls the swapped HN source, writes
// a cache file, and renders one of the two stub stories. It does NOT
// assert which story wins — that's the ranker's business, covered by the
// warm-cache win-rate tests below. This test exists so a regression in
// the fetch-on-miss code path (cache.Read error, HTTP wiring, writeCache)
// fails loudly without depending on math/rand's implementation details.
func TestRunDefault_ColdStart_FetchesAndCaches(t *testing.T) {
	isolateXDG(t)

	ts := algoliaStub()
	defer ts.Close()
	swapHNSource(t, ts.URL)

	var stdout, stderr bytes.Buffer
	rng := rand.New(rand.NewSource(1))
	if err := runDefault(&stdout, &stderr, nil, rng); err != nil {
		t.Fatalf("runDefault: %v\nstderr: %s", err, stderr.String())
	}

	path, err := cache.Path()
	if err != nil {
		t.Fatalf("cache.Path: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cold-start should have written cache at %s: %v", path, err)
	}

	out := stdout.String()
	if !strings.Contains(out, "React 21") && !strings.Contains(out, "Rust 1.87") {
		t.Errorf("expected one of the two stub stories in output; got:\n%s", out)
	}
}

// seedTwoStoryCache writes a fresh cache containing the React+Rust stories
// used by the warm-cache win-rate tests. Ages match algoliaStub so
// rank.Score yields: React ≈ 33, Rust unboosted ≈ 17, Rust boosted ≈ 33.
func seedTwoStoryCache(t *testing.T, now time.Time) {
	t.Helper()
	path, err := cache.Path()
	if err != nil {
		t.Fatalf("cache.Path: %v", err)
	}
	if err := cache.Write(path, &cache.File{
		Version:         cache.SchemaVersion,
		CachedByVersion: defaults.Version,
		FetchedAt:       now.Add(-1 * time.Minute), // fresh: inside 30m TTL
		Stories: []fetch.Story{
			{
				ID: "1", Title: "React 21 drops with native signals",
				URL: "https://reactjs.org/", Source: "hackernews",
				Points: 400, Author: "alice",
				CreatedAt: now.Add(-2 * time.Hour), Tags: []string{},
			},
			{
				ID: "2", Title: "Rust 1.87 stabilizes async closures",
				URL: "https://rust-lang.org/", Source: "hackernews",
				Points: 300, Author: "bob",
				CreatedAt: now.Add(-3 * time.Hour), Tags: []string{},
			},
		},
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
}

// TestRunDefault_WarmCache_TopicBoostFavorsMatch_WinRate checks that the
// 2x topic multiplier shifts the weighted picker toward the matching
// story over N=100 runs. With React ≈ 33 and Rust boosted ≈ 33, the
// theoretical Rust win rate is ~0.50 (σ≈5 at N=100). Without the boost
// Rust's share collapses to ~0.33. Threshold 40 sits ~2σ below the
// boosted mean (≈2% flake) and ~1.4σ above the unboosted mean (so a
// broken boost still usually fails). The asymmetric margin is deliberate:
// CI flakiness is expensive; a missed regression here is also caught by
// unit tests in internal/rank.
func TestRunDefault_WarmCache_TopicBoostFavorsMatch_WinRate(t *testing.T) {
	_, configDir := isolateXDG(t)
	cfgPath := filepath.Join(configDir, "newsfetch", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(`topics = ["rust"]`+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	seedTwoStoryCache(t, time.Now().UTC())

	const N = 100
	rustWins := 0
	for i := range N {
		var stdout, stderr bytes.Buffer
		rng := rand.New(rand.NewSource(int64(i)))
		if err := runDefault(&stdout, &stderr, nil, rng); err != nil {
			t.Fatalf("iter %d: runDefault: %v\nstderr: %s", i, err, stderr.String())
		}
		if strings.Contains(stdout.String(), "Rust 1.87") {
			rustWins++
		}
	}
	if rustWins < 40 {
		t.Errorf("Rust won %d/%d with topics=[\"rust\"]; want >= 40", rustWins, N)
	}
}

// TestRunDefault_WarmCache_TopicsFlagEmptyOverridesConfig_WinRate checks
// that --topics= (explicit empty) defeats the config's topics=["rust"].
// With no boost React's raw 400pts/2h dominates Rust's 300pts/3h; the
// theoretical React win rate is ~0.67 (σ≈4.7 at N=100). Threshold 55
// sits ~2.5σ below the mean (≈0.6% flake). If the flag were silently
// ignored and Rust got the boost, React's expected wins would drop to
// ~50 → 55 is ~1σ above, so the test catches that regression ~84% of
// the time (per-run basis; CI runs accumulate).
func TestRunDefault_WarmCache_TopicsFlagEmptyOverridesConfig_WinRate(t *testing.T) {
	_, configDir := isolateXDG(t)
	cfgPath := filepath.Join(configDir, "newsfetch", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(`topics = ["rust"]`+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	seedTwoStoryCache(t, time.Now().UTC())

	const N = 100
	reactWins := 0
	for i := range N {
		var stdout, stderr bytes.Buffer
		rng := rand.New(rand.NewSource(int64(i)))
		if err := runDefault(&stdout, &stderr, []string{"--topics="}, rng); err != nil {
			t.Fatalf("iter %d: runDefault: %v\nstderr: %s", i, err, stderr.String())
		}
		if strings.Contains(stdout.String(), "React 21") {
			reactWins++
		}
	}
	if reactWins < 55 {
		t.Errorf("React won %d/%d with --topics= defeating config; want >= 55", reactWins, N)
	}
}

func TestRunDefault_StyleJSON_WithInvalidConfig_StdoutIsCleanJSON(t *testing.T) {
	_, configDir := isolateXDG(t)
	cfgPath := filepath.Join(configDir, "newsfetch", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte("style = 'boxed\nbroken"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ts := algoliaStub()
	defer ts.Close()
	swapHNSource(t, ts.URL)

	var stdout, stderr bytes.Buffer
	rng := rand.New(rand.NewSource(1))
	if err := runDefault(&stdout, &stderr, []string{"--style=json"}, rng); err != nil {
		t.Fatalf("runDefault: %v", err)
	}
	// R-3: --style=json is a uniform top-level ARRAY with a pool field on
	// every object. The bare-object-at-count-1 shape this test used to
	// assert was removed deliberately.
	var payload []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("stdout not a parseable JSON array: %v\nstdout: %q\nstderr: %q", err, stdout.String(), stderr.String())
	}
	if len(payload) != 1 {
		t.Fatalf("got %d elements, want 1 (default count): %s", len(payload), stdout.String())
	}
	for _, key := range []string{"title", "url", "source", "age_seconds", "tags", "pool"} {
		if _, ok := payload[0][key]; !ok {
			t.Errorf("missing key %q in JSON output: %s", key, stdout.String())
		}
	}
	if payload[0]["pool"] != "news" {
		t.Errorf("pool = %v, want \"news\"", payload[0]["pool"])
	}
	// stderr must carry the one-line warning.
	if !strings.Contains(stderr.String(), "newsfetch:") {
		t.Errorf("expected warning on stderr; got %q", stderr.String())
	}
}

// writeRefreshConfig writes a config.toml into the isolated
// XDG_CONFIG_HOME. runRefresh loads its own config from disk — there is no
// injection seam — so a pool-level test has to go through a real file.
func writeRefreshConfig(t *testing.T, feedURLs []string, aggregators []string) {
	t.Helper()
	path, err := config.Path()
	if err != nil {
		t.Fatalf("config.Path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	var b strings.Builder
	pools := `pools = ["news", "following"]`
	if len(feedURLs) == 0 {
		pools = `pools = ["news"]`
	}
	fmt.Fprintf(&b, "%s\n\n[news]\naggregators = [", pools)
	for i, a := range aggregators {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q", a)
	}
	b.WriteString("]\n")
	for _, u := range feedURLs {
		fmt.Fprintf(&b, "\n[[following.feeds]]\nurl = %q\n", u)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// failIfFollowingBuilt mirrors the newSource defence the two refresh-lock
// tests use (main_test.go:189-193): if a regression reaches the following
// fan-out when it must not, the test fails instead of silently issuing N
// feed requests. The returned client has no feeds, so FetchFeeds does
// nothing even if the guard is reached.
func failIfFollowingBuilt(t *testing.T, why string) {
	t.Helper()
	original := newFollowing
	newFollowing = func(specs []fetch.FeedSpec, validators func(string) (string, string)) *fetch.Following {
		t.Errorf("runRefresh built the following fan-out (%d feeds) %s", len(specs), why)
		return &fetch.Following{}
	}
	t.Cleanup(func() { newFollowing = original })
}

// rssServer serves one dated RSS item at /feed.xml, or 500 everywhere when
// broken is true.
func rssServer(t *testing.T, broken bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if broken {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<rss version="2.0"><channel><title>T</title>`+
			`<item><title>Feed story</title><link>https://feed.example.com/one</link>`+
			`<description>d</description><pubDate>2026-09-01T10:00:00Z</pubDate></item>`+
			`</channel></rss>`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func brokenAlgoliaStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func readRefreshLog(t *testing.T) string {
	t.Helper()
	path, err := refreshlog.Path()
	if err != nil {
		t.Fatalf("refreshlog.Path: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read refresh log: %v", err)
	}
	return string(data)
}

// TestRunRefresh_HeldLockSkipsEveryPool extends the single-flight guard to
// the multi-pool body: with the following pool ENABLED in config, a held
// lock must still build neither a Source nor the feed fan-out. The two
// pre-existing lock tests run with the default (news-only) config and
// therefore cannot see a following-pool regression.
func TestRunRefresh_HeldLockSkipsEveryPool(t *testing.T) {
	isolateXDG(t)
	feeds := rssServer(t, false)
	writeRefreshConfig(t, []string{feeds.URL + "/feed.xml"}, []string{"hackernews"})

	dir, err := cache.Dir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	held, err := lockfile.Acquire(filepath.Join(dir, "refresh.lock"), time.Second)
	if err != nil {
		t.Fatalf("test could not take the refresh lock: %v", err)
	}
	t.Cleanup(func() { held.Close() })

	original := newSource
	newSource = func(name string) (fetch.Source, error) {
		t.Errorf("runRefresh built source %q while another refresh held the lock", name)
		return nil, fmt.Errorf("source %q must not be built", name)
	}
	t.Cleanup(func() { newSource = original })
	failIfFollowingBuilt(t, "while another refresh held the lock")

	if err := runRefresh(); err != nil {
		t.Errorf("runRefresh() = %v, want nil (another refresh is already in flight)", err)
	}
}

// TestRunRefresh_NewsFailureDoesNotBlockFollowing pins R-29's per-pool
// isolation in the direction that used to be fatal: before this change a
// news pool that fetched nothing returned an error and the process exited
// 1 with the following pool never refreshed.
func TestRunRefresh_NewsFailureDoesNotBlockFollowing(t *testing.T) {
	isolateXDG(t)
	feeds := rssServer(t, false)
	writeRefreshConfig(t, []string{feeds.URL + "/feed.xml"}, []string{"hackernews"})
	swapHNSource(t, brokenAlgoliaStub(t).URL)

	if err := runRefresh(); err != nil {
		t.Fatalf("runRefresh() = %v, want nil: the following pool succeeded", err)
	}

	newsPath, err := cache.PoolPath("news")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(newsPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("feed.json exists after a failed news fetch (stat err = %v); a failed pool must not write", err)
	}
	followingPath, err := cache.PoolPath("following")
	if err != nil {
		t.Fatal(err)
	}
	f, err := cache.Read(followingPath)
	if err != nil {
		t.Fatalf("following.json should have been written: %v", err)
	}
	if len(f.Stories) != 1 || f.Stories[0].URL != "https://feed.example.com/one" {
		t.Errorf("following.json stories = %+v, want the one fetched feed story", f.Stories)
	}
}

// TestRunRefresh_FollowingFailureDoesNotBlockNews is the mirror case: the
// feed server is down, the aggregator is up, and feed.json must still be
// written with exit status 0.
func TestRunRefresh_FollowingFailureDoesNotBlockNews(t *testing.T) {
	isolateXDG(t)
	feeds := rssServer(t, true)
	writeRefreshConfig(t, []string{feeds.URL + "/feed.xml"}, []string{"hackernews"})
	swapHNSource(t, algoliaStub().URL)

	if err := runRefresh(); err != nil {
		t.Fatalf("runRefresh() = %v, want nil: the news pool succeeded", err)
	}

	newsPath, err := cache.PoolPath("news")
	if err != nil {
		t.Fatal(err)
	}
	f, err := cache.Read(newsPath)
	if err != nil {
		t.Fatalf("feed.json should have been written: %v", err)
	}
	if len(f.Stories) == 0 {
		t.Error("feed.json has no stories; the news pool wrote an empty cache")
	}
	followingPath, err := cache.PoolPath("following")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(followingPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("following.json exists after every feed failed (stat err = %v); a failed pool must not write", err)
	}
}

// TestRunRefresh_EveryPoolFailingReturnsError is the other half of the new
// exit contract: exit 1 now means every enabled pool failed, so this is the
// only shape that may still return an error.
func TestRunRefresh_EveryPoolFailingReturnsError(t *testing.T) {
	isolateXDG(t)
	feeds := rssServer(t, true)
	writeRefreshConfig(t, []string{feeds.URL + "/feed.xml"}, []string{"hackernews"})
	swapHNSource(t, brokenAlgoliaStub(t).URL)

	if err := runRefresh(); err == nil {
		t.Fatal("runRefresh() = nil with every enabled pool failing, want an error so the process exits 1")
	}
}

// TestRunRefresh_LogLinesCarryPoolNamespaces pins R-29/R-30: FetchAll keys
// its error map by SOURCE NAME and FetchFeeds keys its own by FEED URL, so
// every line has to say which namespace it came from or the log is
// ambiguous the moment a feed host is called "hackernews".
func TestRunRefresh_LogLinesCarryPoolNamespaces(t *testing.T) {
	isolateXDG(t)
	feeds := rssServer(t, true)
	feedURL := feeds.URL + "/feed.xml"
	writeRefreshConfig(t, []string{feedURL}, []string{"hackernews"})
	swapHNSource(t, brokenAlgoliaStub(t).URL)

	_ = runRefresh() // both pools fail; the log is what this test reads

	log := readRefreshLog(t)
	for _, want := range []string{
		"news hackernews: ",          // per-source failure, news namespace
		"news: ",                     // the news pool itself fetched nothing
		"following " + feedURL + ":", // per-feed failure, following namespace
		"following: ",                // the following pool itself failed
	} {
		if !strings.Contains(log, want) {
			t.Errorf("refresh.log missing a %q line:\n%s", want, log)
		}
	}
}

// TestRunRefresh_NewsErrorsReachTheLogWithContext pins the error wrapping
// the global constraints require of refreshNews. Its two failure paths —
// multiFetch and writeCache — both end up in refresh.log behind
// runRefresh's "news: " prefix, and an error returned bare from either
// site names no operation at all: "news: rename temp cache: ..." leaves a
// reader guessing which file, and multiFetch's raw error leaves them
// guessing which stage. Both configs here are news-only, so the failing
// pool is also the only ACTIVE one and runRefresh must return an error.
func TestRunRefresh_NewsErrorsReachTheLogWithContext(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T)
		want  string
	}{
		{
			name: "source cannot be built",
			setup: func(t *testing.T) {
				original := newSource
				newSource = func(name string) (fetch.Source, error) {
					return nil, errors.New("no such source")
				}
				t.Cleanup(func() { newSource = original })
			},
			want: "news: fetch aggregators: ",
		},
		{
			name: "cache path is occupied by a directory",
			setup: func(t *testing.T) {
				srv := algoliaStub()
				t.Cleanup(srv.Close)
				swapHNSource(t, srv.URL)
				path, err := cache.PoolPath("news")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: "news: write feed.json: ",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateXDG(t)
			writeRefreshConfig(t, nil, []string{"hackernews"})
			tc.setup(t)

			if err := runRefresh(); err == nil {
				t.Fatal("runRefresh() = nil with the only active pool failing, want an error")
			}
			if log := readRefreshLog(t); !strings.Contains(log, tc.want) {
				t.Errorf("refresh.log missing a %q line:\n%s", tc.want, log)
			}
		})
	}
}

// writeRefreshConfigExplicitPools writes a config.toml with a caller-chosen
// pools list, independent of whether news or following actually has
// content. writeRefreshConfig infers pools from whether feedURLs is empty
// and so can never express "pool enabled, pool empty" for following (its
// pools list drops "following" outright when there are no feeds) — exactly
// the shape the enabled-vs-active tests below need, because that shape is
// the one PoolEnabled and NewsActive/FollowingActive disagree on.
func writeRefreshConfigExplicitPools(t *testing.T, pools, feedURLs, aggregators []string) {
	t.Helper()
	path, err := config.Path()
	if err != nil {
		t.Fatalf("config.Path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	quoted := make([]string, len(pools))
	for i, p := range pools {
		quoted[i] = fmt.Sprintf("%q", p)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "pools = [%s]\n\n[news]\naggregators = [", strings.Join(quoted, ", "))
	for i, a := range aggregators {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q", a)
	}
	b.WriteString("]\n")
	for _, u := range feedURLs {
		fmt.Fprintf(&b, "\n[[following.feeds]]\nurl = %q\n", u)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// refreshLogOrEmpty reads refresh.log like readRefreshLog, but a missing
// file is not a test failure: unlike the existing failure-path tests, the
// two tests below expect a genuinely quiet run (a skipped pool logs
// nothing, and the other pool succeeds without a hitch), so refresh.log may
// never have been created at all when the implementation is correct.
func refreshLogOrEmpty(t *testing.T) string {
	t.Helper()
	path, err := refreshlog.Path()
	if err != nil {
		t.Fatalf("refreshlog.Path: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read refresh log: %v", err)
	}
	return string(data)
}

// TestRunRefresh_NewsEnabledButEmptyIsSkippedNotFailed pins the
// enabled-vs-active distinction at runRefresh's news call site. It is not
// covered by any earlier test: every prior writeRefreshConfig call gives
// news a non-empty aggregator list whenever news is enabled, so "enabled"
// and "active" never diverge for news anywhere else in this file. A
// regression that swaps cfg.NewsActive() for cfg.PoolEnabled("news") would
// still see news as enabled here (it IS enabled, just with nothing
// configured) and would call refreshNews anyway, which returns "all
// aggregators returned no stories" for a genuinely empty aggregator list —
// producing a spurious "news: ..." log line for a pool that was never
// supposed to run at all.
func TestRunRefresh_NewsEnabledButEmptyIsSkippedNotFailed(t *testing.T) {
	isolateXDG(t)
	feeds := rssServer(t, false)
	writeRefreshConfigExplicitPools(t,
		[]string{"news", "following"},
		[]string{feeds.URL + "/feed.xml"},
		nil, // news enabled, zero aggregators: enabled but not active
	)

	if err := runRefresh(); err != nil {
		t.Fatalf("runRefresh() = %v, want nil: following is active and succeeds, news is enabled but has nothing to do", err)
	}

	followingPath, err := cache.PoolPath("following")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(followingPath); err != nil {
		t.Errorf("following.json should have been written: %v", err)
	}
	if log := refreshLogOrEmpty(t); strings.Contains(log, "news") {
		t.Errorf("refresh.log has a news-pool line for a pool with nothing configured (enabled != active):\n%s", log)
	}
}

// TestRunRefresh_FollowingEnabledButEmptyDoesNotMaskNewsFailure pins the
// enabled-vs-active distinction at runRefresh's FOLLOWING call site.
//
// The literal mirror of the news test above — following empty, news
// enabled and SUCCEEDING — cannot discriminate the
// cfg.FollowingActive()→cfg.PoolEnabled("following") regression:
// refreshFollowing carries its own defense-in-depth check
// (cfg.FollowingActive(), its very first line, there to protect
// feedstate.Update's garbage-collection — see its doc comment) that no-ops
// the whole fan-out regardless of which gate runRefresh used to call it. An
// empty-but-enabled following pool stays silent either way as long as
// nothing else is failing, so that shape was tried, observed to still PASS
// under the mutation below, and dropped rather than committed as a false
// regression pin.
//
// The distinction only becomes externally visible when it changes the
// all-active-pools-failed verdict: pair the enabled-but-empty following
// pool with a FAILING (not succeeding) news pool. Correctly, exactly one
// pool is ACTIVE (news) and it failed, so runRefresh must return an error.
// Under the regression, following is wrongly counted into "attempted"
// (PoolEnabled("following") is true) but refreshFollowing's internal guard
// still keeps it out of "failed" — diluting attempted=2/failed=1 into a
// false success (nil, exit 0) for a user whose only working pool just
// broke.
func TestRunRefresh_FollowingEnabledButEmptyDoesNotMaskNewsFailure(t *testing.T) {
	isolateXDG(t)
	swapHNSource(t, brokenAlgoliaStub(t).URL)
	writeRefreshConfigExplicitPools(t,
		[]string{"news", "following"},
		nil, // following enabled, zero feeds: enabled but not active
		[]string{"hackernews"},
	)

	if err := runRefresh(); err == nil {
		t.Fatal("runRefresh() = nil with the only ACTIVE pool (news) failing, want an error: an enabled-but-empty following pool must not dilute the all-active-pools-failed verdict")
	}

	newsPath, err := cache.PoolPath("news")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(newsPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("feed.json exists after a failed news fetch (stat err = %v); a failed pool must not write", err)
	}
	if log := refreshLogOrEmpty(t); strings.Contains(log, "following") {
		t.Errorf("refresh.log has a following-pool line for a pool with nothing configured (enabled != active):\n%s", log)
	}
}

// TestRunDefault_SinglePoolRenderIsByteExact is the header-leak guard.
// TestRunDefault_RendersFromFreshCache above asserts substrings only and
// would pass happily with a "╭─ News ─╮" header printed over the box; the
// single-pool experience must stay byte-identical to the pre-pool render,
// which means a bare render.Boxed and nothing else.
func TestRunDefault_SinglePoolRenderIsByteExact(t *testing.T) {
	isolateXDG(t)
	original := spawnRefresh
	spawnRefresh = func() { t.Error("fresh cache must not spawn a refresh") }
	t.Cleanup(func() { spawnRefresh = original })

	now := time.Now().UTC()
	story := fetch.Story{
		ID:        "hn-1",
		Title:     "A seeded story",
		URL:       "https://example.com/x",
		Source:    "hackernews",
		Points:    100,
		Author:    "alice",
		CreatedAt: now.Add(-2 * time.Hour),
		Tags:      []string{},
	}
	path, err := cache.PoolPath("news")
	if err != nil {
		t.Fatalf("cache.PoolPath: %v", err)
	}
	if err := cache.Write(path, &cache.File{
		Version:         cache.SchemaVersion,
		CachedByVersion: defaults.Version,
		FetchedAt:       now.Add(-5 * time.Minute),
		Stories:         []fetch.Story{story},
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	var buf bytes.Buffer
	if err := runDefault(&buf, io.Discard, nil, rand.New(rand.NewSource(1))); err != nil {
		t.Fatalf("runDefault: %v", err)
	}
	want := render.Boxed(story, now, defaults.TermWidth(defaults.BoxWidth))
	if buf.String() != want {
		t.Errorf("single-pool render is no longer byte-identical to Boxed\n got: %q\nwant: %q", buf.String(), want)
	}
}

// TestRunDefault_TwoPoolsStackInPoolOrder is the end-to-end counterpart:
// two seeded cache files, two labelled boxes, following first.
func TestRunDefault_TwoPoolsStackInPoolOrder(t *testing.T) {
	_, configDir := isolateXDG(t)
	original := spawnRefresh
	spawnRefresh = func() {}
	t.Cleanup(func() { spawnRefresh = original })

	cfgPath := filepath.Join(configDir, "newsfetch", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	toml := `pools = ["news", "following"]
pool_order = ["following", "news"]
count = 1
following_count = 1

[news]
aggregators = ["hackernews"]

[[following.feeds]]
url = "https://a.example/feed.xml"
`
	if err := os.WriteFile(cfgPath, []byte(toml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	now := time.Now().UTC()
	writePool := func(pool string, stories []fetch.Story) {
		p, err := cache.PoolPath(pool)
		if err != nil {
			t.Fatalf("cache.PoolPath(%q): %v", pool, err)
		}
		if err := cache.Write(p, &cache.File{
			Version:         cache.SchemaVersion,
			CachedByVersion: defaults.Version,
			FetchedAt:       now.Add(-time.Minute),
			Stories:         stories,
		}); err != nil {
			t.Fatalf("seed %s cache: %v", pool, err)
		}
	}
	writePool("news", []fetch.Story{{
		ID: "hn-1", Title: "React 21 drops with native signals",
		URL: "https://reactjs.org/", Source: "hackernews", Points: 400,
		CreatedAt: now.Add(-2 * time.Hour), Tags: []string{},
	}})
	writePool("following", []fetch.Story{{
		ID: "f-1", Title: "The case for slow blogging",
		URL: "https://drewdevault.com/slow", Source: "following",
		Feed:      "https://a.example/feed.xml",
		CreatedAt: now.Add(-48 * time.Hour), Tags: []string{},
	}})

	var stdout, stderr bytes.Buffer
	if err := runDefault(&stdout, &stderr, nil, rand.New(rand.NewSource(1))); err != nil {
		t.Fatalf("runDefault: %v\nstderr: %s", err, stderr.String())
	}
	out := stdout.String()
	followingAt := strings.Index(out, "╭─ Following ─")
	newsAt := strings.Index(out, "╭─ News ─")
	if followingAt < 0 || newsAt < 0 {
		t.Fatalf("expected both labelled box headers; got:\n%s", out)
	}
	if followingAt > newsAt {
		t.Errorf("pools stacked out of pool_order; got:\n%s", out)
	}
	for _, want := range []string{"The case for slow blogging", "React 21 drops with native signals"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

// TestRunDefault_StyleJSON_NothingToShowIsAnEmptyArray is the end-to-end
// half of the same fix. The reachable configuration needs no network and
// no failure: one enabled pool whose cache read cleanly and held zero
// stories. Before the fix runDefault printed the offline sentence with no
// style dispatch at all, so a --style=json consumer piping into jq broke
// on a perfectly healthy install.
func TestRunDefault_StyleJSON_NothingToShowIsAnEmptyArray(t *testing.T) {
	_, configDir := isolateXDG(t)
	original := spawnRefresh
	spawnRefresh = func() {}
	t.Cleanup(func() { spawnRefresh = original })

	cfgPath := filepath.Join(configDir, "newsfetch", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	toml := "pools = [\"following\"]\n\n[[following.feeds]]\nurl = \"https://a.example/feed.xml\"\n"
	if err := os.WriteFile(cfgPath, []byte(toml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	now := time.Now().UTC()
	path, err := cache.PoolPath("following")
	if err != nil {
		t.Fatalf("cache.PoolPath: %v", err)
	}
	if err := cache.Write(path, &cache.File{
		Version:         cache.SchemaVersion,
		CachedByVersion: defaults.Version,
		FetchedAt:       now.Add(-time.Minute),
		Stories:         []fetch.Story{},
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := runDefault(&stdout, &stderr, []string{"--style=json"}, rand.New(rand.NewSource(1))); err != nil {
		t.Fatalf("runDefault: %v\nstderr: %s", err, stderr.String())
	}
	var payload []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("--style=json emitted non-JSON with nothing to show: %v\nstdout: %q", err, stdout.String())
	}
	if len(payload) != 0 {
		t.Errorf("got %d elements, want 0: %s", len(payload), stdout.String())
	}
}

// TestSettingsAnswers_PreservesUnsetFeedKnobs pins the read half of the
// --settings round trip. config.FeedConfig uses a zero value to mean
// "unset"; onboard.Feed uses a nil pointer. Mapping an unset knob to a
// non-nil zero pointer would make the writer emit `max_items = 0` into the
// user's file — a value they never chose and one the loader clamps.
func TestSettingsAnswers_PreservesUnsetFeedKnobs(t *testing.T) {
	cases := []struct {
		name         string
		in           config.FeedConfig
		wantMaxItems *int
		wantWeight   *float64
	}{
		{"both unset", config.FeedConfig{URL: "https://a.example/f"}, nil, nil},
		{"max_items only", config.FeedConfig{URL: "https://a.example/f", MaxItems: 2}, intp(2), nil},
		{"weight only", config.FeedConfig{URL: "https://a.example/f", Weight: 0.3}, nil, floatp(0.3)},
		{"both set", config.FeedConfig{URL: "https://a.example/f", MaxItems: 10, Weight: 5}, intp(10), floatp(5)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Following.Feeds = []config.FeedConfig{tc.in}
			got := settingsAnswers(cfg)
			if len(got.Feeds) != 1 {
				t.Fatalf("Feeds = %+v, want exactly one", got.Feeds)
			}
			if got.Feeds[0].URL != tc.in.URL {
				t.Errorf("URL = %q, want %q", got.Feeds[0].URL, tc.in.URL)
			}
			if !reflect.DeepEqual(got.Feeds[0].MaxItems, tc.wantMaxItems) {
				t.Errorf("MaxItems = %v, want %v", derefInt(got.Feeds[0].MaxItems), derefInt(tc.wantMaxItems))
			}
			if !reflect.DeepEqual(got.Feeds[0].Weight, tc.wantWeight) {
				t.Errorf("Weight = %v, want %v", derefFloat(got.Feeds[0].Weight), derefFloat(tc.wantWeight))
			}
		})
	}
}

func intp(n int) *int { return &n }

func floatp(f float64) *float64 { return &f }

func derefInt(p *int) string {
	if p == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%d", *p)
}

func derefFloat(p *float64) string {
	if p == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%v", *p)
}

// TestSettingsAnswers_ConfigRoundTripIsByteIdentical is the regression guard
// for the whole task. A --settings run that changes nothing must rewrite the
// user's file to exactly the bytes it started with; any advanced per-feed
// knob dropped anywhere between config.Load and renderConfigTOML shows up
// here as a diff. The fixture is written in renderConfigTOML's own output
// format so the comparison is a genuine identity, not an approximation.
func TestSettingsAnswers_ConfigRoundTripIsByteIdentical(t *testing.T) {
	const fixture = `# newsfetch config. Edit freely; see spec.md for field meanings.

topics = ["rust"]
style = "boxed"
pools = ["news", "following"]
pool_order = ["following", "news"]
count = 2
following_count = 1
ticker_marker = "dot"
ticker_boxed = false

[news]
aggregators = ["hackernews"]

[[following.feeds]]
url = "https://drewdevault.com/blog/index.xml"

[[following.feeds]]
url = "https://blog.cloudflare.com/rss/"
max_items = 2
weight = 0.3
`
	dir := t.TempDir()
	src := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(src, []byte(fixture), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	cfg, err := config.Load(src)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	out := filepath.Join(dir, "rewritten.toml")
	if err := onboard.OverwriteConfig(out, settingsAnswers(cfg)); err != nil {
		t.Fatalf("OverwriteConfig: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read rewritten config: %v", err)
	}
	if string(got) != fixture {
		t.Errorf("round trip changed the config file\n--- got ---\n%s\n--- want ---\n%s", got, fixture)
	}
}

// TestSettingsAnswers_NonFiniteWeightNeverReachesTheFile walks the one path
// that skips every weight validator. --settings loads config.toml through
// config.Load WITHOUT calling config.Validate, so a hand-written
// `weight = nan` — which TOML accepts as a float literal — arrives in
// settingsAnswers as a genuine NaN, becomes a non-nil *float64 (NaN != 0),
// and lands in the writer. tomlFloat is the last guard, and this is the
// only test that exercises it end to end: without it the rewritten file
// would say `weight = NaN.0`, which is not valid TOML, and the user's next
// terminal open would fail to load their config at all.
func TestSettingsAnswers_NonFiniteWeightNeverReachesTheFile(t *testing.T) {
	const fixture = `topics = ["rust"]
style = "boxed"
pools = ["news", "following"]
count = 1
following_count = 1

[news]
aggregators = ["hackernews"]

[[following.feeds]]
url = "https://a.example/f"
weight = nan
`
	dir := t.TempDir()
	src := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(src, []byte(fixture), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	cfg, err := config.Load(src)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	a := settingsAnswers(cfg)
	// Guard the guard: if the projection ever starts dropping NaN, this
	// test would pass for the wrong reason and stop covering tomlFloat.
	if len(a.Feeds) != 1 || a.Feeds[0].Weight == nil || !math.IsNaN(*a.Feeds[0].Weight) {
		t.Fatalf("fixture no longer carries a NaN weight into the writer: %+v", a.Feeds)
	}
	out := filepath.Join(dir, "rewritten.toml")
	if err := onboard.OverwriteConfig(out, a); err != nil {
		t.Fatalf("OverwriteConfig: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read rewritten config: %v", err)
	}
	if strings.Contains(string(data), "weight") {
		t.Errorf("a non-finite weight was written back:\n%s", data)
	}
	// The real damage is unloadability, so assert that directly rather than
	// trusting the substring check to have caught every bad spelling.
	back, err := config.Load(out)
	if err != nil {
		t.Fatalf("rewritten config no longer loads: %v", err)
	}
	if len(back.Following.Feeds) != 1 {
		t.Fatalf("Following.Feeds = %+v, want the feed preserved", back.Following.Feeds)
	}
	if back.Following.Feeds[0].Weight != 0 {
		t.Errorf("Weight = %v, want 0 (omitted means no manual override)", back.Following.Feeds[0].Weight)
	}
}

// TestSettingsAnswers_ExplicitZeroSentinelNeverEscapesToDisk pins the fix
// round 1 bug: config.Load encodes a max_items/weight the user typed as
// literal 0 as the internal -1 "explicit zero" sentinel (see
// config.explicitZeroMarker) so config.Validate can tell that apart from an
// absent key. Every existing "unset" case up to this point constructed
// config.FeedConfig directly in Go, which produces a real zero and never
// exercises the sentinel — this test instead seeds an actual config FILE and
// decodes it through config.Load, the only place the sentinel is ever
// produced.
//
// settingsAnswers is called directly on the UNVALIDATED config.Load output
// (no config.Validate in between) on purpose: Current now always validates
// first, but settingsAnswers's own guard must be correct independent of
// that — "guard regardless" — so this test exercises exactly the call
// sequence that leaked the sentinel before this fix:
// config.Load -> settingsAnswers -> OverwriteConfig.
func TestSettingsAnswers_ExplicitZeroSentinelNeverEscapesToDisk(t *testing.T) {
	const fixture = `topics = ["rust"]
style = "boxed"
pools = ["news", "following"]
count = 1
following_count = 1

[news]
aggregators = ["hackernews"]

[[following.feeds]]
url = "https://a.example/explicit-zero"
max_items = 0
weight = 0

[[following.feeds]]
url = "https://a.example/absent"

[[following.feeds]]
url = "https://a.example/valid"
max_items = 5
weight = 2.5
`
	dir := t.TempDir()
	src := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(src, []byte(fixture), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	cfg, err := config.Load(src)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	// Guard the guard: if config.Load ever stops producing the sentinel for
	// an explicit zero, this test would pass without exercising the bug.
	if cfg.Following.Feeds[0].MaxItems != -1 || cfg.Following.Feeds[0].Weight != -1 {
		t.Fatalf("fixture no longer decodes to the explicit-zero sentinel: %+v", cfg.Following.Feeds[0])
	}
	a := settingsAnswers(cfg)
	out := filepath.Join(dir, "rewritten.toml")
	if err := onboard.OverwriteConfig(out, a); err != nil {
		t.Fatalf("OverwriteConfig: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read rewritten config: %v", err)
	}
	if strings.Contains(string(data), "max_items = -1") || strings.Contains(string(data), "weight = -1") {
		t.Errorf("the explicit-zero sentinel escaped into the rewritten config:\n%s", data)
	}
	back, err := config.Load(out)
	if err != nil {
		t.Fatalf("rewritten config no longer loads: %v", err)
	}
	want := []config.FeedConfig{
		{URL: "https://a.example/explicit-zero"},
		{URL: "https://a.example/absent"},
		{URL: "https://a.example/valid", MaxItems: 5, Weight: 2.5},
	}
	if !reflect.DeepEqual(back.Following.Feeds, want) {
		t.Errorf("Following.Feeds = %+v, want %+v", back.Following.Feeds, want)
	}
}

// TestSettingsAnswers_InvalidFeedURLSurvivesUnvalidated pins fix round 2's
// reversal of fix round 1: settingsCurrent (SettingsDeps.Current's
// production implementation) must NOT run the loaded config through
// config.Validate before projecting it into Answers.
//
// Fix round 1 added that Validate call to resolve config.Load's internal
// "explicit zero" sentinel (see TestSettingsAnswers_ExplicitZeroSentinelNeverEscapesToDisk
// above) into a real value. That traded a bounded numeric corruption for a
// worse bug: config.Validate's splitFeeds drops any feed whose URL is
// unparseable, relative, or missing a supported scheme (see
// internal/config/validate.go's feedURLProblem) — meant to keep unusable
// feeds off the render/fetch path. But OverwriteConfig regenerates the
// ENTIRE config file from Answers, so validating in settingsCurrent would
// make the very next `--settings` save silently DELETE that feed's line,
// with no warning (the writer is discarded). A mistyped URL is the one
// part of a feed entry a user cannot reconstruct from memory, unlike an
// out-of-range max_items/weight, which is just a number Validate would
// clamp back into range. A user who mistypes a URL today sees a per-render
// warning and can go fix the character; they must not lose the line
// entirely just because they ran --settings for something unrelated.
//
// This test calls settingsCurrent itself rather than re-implementing its
// two steps (config.Load then settingsAnswers) inline. Fix round 2's first
// version did the latter, with a comment claiming it matched Current
// "exactly" — and it passed even after the coordinator re-applied the
// mistake to the real closure, because a re-implementation cannot observe
// a change to the thing it re-implements. Driving settingsCurrent directly
// is what makes this test able to catch config.Validate being reintroduced
// there.
func TestSettingsAnswers_InvalidFeedURLSurvivesUnvalidated(t *testing.T) {
	const fixture = `# newsfetch config. Edit freely; see spec.md for field meanings.

topics = ["rust"]
style = "boxed"
pools = ["news", "following"]
pool_order = ["following", "news"]
count = 1
following_count = 1
ticker_marker = "dot"
ticker_boxed = false

[news]
aggregators = ["hackernews"]

[[following.feeds]]
url = "example.com/feed.xml"
`
	dir := t.TempDir()
	src := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(src, []byte(fixture), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	// Prove the premise: this URL is exactly the shape config.Validate
	// drops. Without this, the test would not demonstrate why
	// settingsCurrent must skip Validate, only that it happens to.
	cfg, err := config.Load(src)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if len(cfg.Following.Feeds) != 1 || cfg.Following.Feeds[0].URL != "example.com/feed.xml" {
		t.Fatalf("fixture did not decode the invalid-URL feed as loaded: %+v", cfg.Following.Feeds)
	}
	validated := config.Validate(cfg, config.FieldSources{}, io.Discard)
	if len(validated.Following.Feeds) != 0 {
		t.Fatalf("fixture URL no longer trips config.Validate's feed-drop rule, test no longer proves anything: %+v", validated.Following.Feeds)
	}
	// Drive the real production path — settingsCurrent itself, the exact
	// function SettingsFlow calls as Current — not a reimplementation of it.
	a, err := settingsCurrent(src)
	if err != nil {
		t.Fatalf("settingsCurrent: %v", err)
	}
	out := filepath.Join(dir, "rewritten.toml")
	if err := onboard.OverwriteConfig(out, a); err != nil {
		t.Fatalf("OverwriteConfig: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read rewritten config: %v", err)
	}
	if string(got) != fixture {
		t.Errorf("a --settings round trip must not delete a feed with an invalid URL\n--- got ---\n%s\n--- want (byte-identical) ---\n%s", got, fixture)
	}
}
