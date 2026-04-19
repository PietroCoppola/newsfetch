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
// near the network. The stale/missing paths need a live upstream or a
// Source-injection seam that M1 doesn't have yet; they are covered by the
// manual smoke test in the Definition of Done.
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

func swapHNSource(t *testing.T, url string) {
	t.Helper()
	original := newHNSource
	newHNSource = func() fetch.Source { return &fetch.HackerNews{BaseURL: url} }
	t.Cleanup(func() { newHNSource = original })
}

func TestRunDefault_ColdStart_HonorsTopicFilter(t *testing.T) {
	cacheDir := t.TempDir()
	configDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	t.Setenv("XDG_CONFIG_HOME", configDir)
	// Write a config that selects rust.
	cfgPath := filepath.Join(configDir, "newsfetch", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(`topics = ["rust"]`+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ts := algoliaStub()
	defer ts.Close()
	swapHNSource(t, ts.URL)

	var stdout, stderr bytes.Buffer
	// seed=42 gives Float64≈0.373, which is below the Rust 2x threshold (~0.501)
	// so the weighted picker reliably selects Rust when the 2x topic boost is active.
	rng := rand.New(rand.NewSource(42))
	if err := runDefault(&stdout, &stderr, nil, rng); err != nil {
		t.Fatalf("runDefault: %v", err)
	}
	if !strings.Contains(stdout.String(), "Rust 1.87") {
		t.Errorf("expected Rust story with topic filter; got stdout:\n%s\nstderr: %s", stdout.String(), stderr.String())
	}
}

func TestRunDefault_ColdStart_TopicsFlagExplicitEmpty(t *testing.T) {
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
	ts := algoliaStub()
	defer ts.Close()
	swapHNSource(t, ts.URL)

	var stdout, stderr bytes.Buffer
	rng := rand.New(rand.NewSource(1))
	// --topics= (explicit empty) should defeat the config's topics=["rust"].
	if err := runDefault(&stdout, &stderr, []string{"--topics="}, rng); err != nil {
		t.Fatalf("runDefault: %v", err)
	}
	// Without the topic boost, React's higher points dominate the pool.
	// We assert React, not "not Rust", so the test fails clearly if the
	// stub changes shape.
	if !strings.Contains(stdout.String(), "React 21") {
		t.Errorf("expected React story when --topics= defeats config; got stdout:\n%s", stdout.String())
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
