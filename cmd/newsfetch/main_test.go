package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/cache"
	"github.com/PietroCoppola/newsfetch/internal/defaults"
	"github.com/PietroCoppola/newsfetch/internal/fetch"
)

// TestRunDefault_RendersFromFreshCache seeds a cache file under
// XDG_CACHE_HOME and verifies runDefault prints a boxed story without going
// near the network. The cold-start fetch-on-miss path is covered by
// TestRunDefault_ColdStart_FetchesAndCaches below; the stochastic
// topic-boost behaviour is covered by the WarmCache win-rate tests.
func TestRunDefault_RendersFromFreshCache(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)

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

func TestFallbackMessage_SingleSourceNamed(t *testing.T) {
	got := fallbackMessage([]string{"lobsters"})
	if !strings.Contains(got, "lobsters") {
		t.Errorf("single-source fallback should name the source; got %q", got)
	}
	if !strings.Contains(got, "check your connection") {
		t.Errorf("fallback should keep the connection hint; got %q", got)
	}
}

func TestFallbackMessage_MultiSourceGeneric(t *testing.T) {
	got := fallbackMessage([]string{"hackernews", "lobsters"})
	if got != defaults.FallbackMessage {
		t.Errorf("multi-source fallback = %q, want default %q", got, defaults.FallbackMessage)
	}
}

func TestFallbackMessage_NoSourcesGeneric(t *testing.T) {
	// Defence-in-depth: cfg.Sources should never be empty post-Validate,
	// but if it ever is, the generic message is the safe choice.
	got := fallbackMessage(nil)
	if got != defaults.FallbackMessage {
		t.Errorf("nil-sources fallback = %q, want default", got)
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
	cacheDir := t.TempDir()
	configDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	t.Setenv("XDG_CONFIG_HOME", configDir)

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
	cacheDir := t.TempDir()
	configDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	t.Setenv("XDG_CONFIG_HOME", configDir)
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
	cacheDir := t.TempDir()
	configDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	t.Setenv("XDG_CONFIG_HOME", configDir)
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
	cacheDir := t.TempDir()
	configDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	t.Setenv("XDG_CONFIG_HOME", configDir)
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
	// stdout must be parseable JSON despite the broken config.
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("stdout not parseable JSON: %v\nstdout: %q\nstderr: %q", err, stdout.String(), stderr.String())
	}
	for _, key := range []string{"title", "url", "source", "age_seconds", "tags"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("missing key %q in JSON output: %s", key, stdout.String())
		}
	}
	// stderr must carry the one-line warning.
	if !strings.Contains(stderr.String(), "newsfetch:") {
		t.Errorf("expected warning on stderr; got %q", stderr.String())
	}
}
