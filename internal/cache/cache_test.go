package cache_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/cache"
	"github.com/PietroCoppola/newsfetch/internal/fetch"
)

func TestWriteRead_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "feed.json")

	want := &cache.File{
		Version:         cache.SchemaVersion,
		CachedByVersion: "test-1.0",
		FetchedAt:       time.Date(2026, 4, 18, 10, 0, 0, 0, time.UTC),
		Stories: []fetch.Story{
			{
				ID:        "hn-1",
				Title:     "A story",
				URL:       "https://example.com/a",
				Source:    "hackernews",
				Points:    100,
				Author:    "alice",
				CreatedAt: time.Date(2026, 4, 18, 9, 0, 0, 0, time.UTC),
			},
			{
				ID:        "hn-2",
				Title:     "Another story",
				URL:       "https://example.com/b",
				Source:    "hackernews",
				Points:    80,
				Author:    "bob",
				CreatedAt: time.Date(2026, 4, 18, 8, 30, 0, 0, time.UTC),
			},
		},
	}

	if err := cache.Write(path, want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := cache.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestFile_IsFresh(t *testing.T) {
	base := time.Date(2026, 4, 18, 10, 0, 0, 0, time.UTC)
	ttl := 30 * time.Minute

	cases := []struct {
		name      string
		fetchedAt time.Time
		now       time.Time
		want      bool
	}{
		{name: "just written", fetchedAt: base, now: base, want: true},
		{name: "within ttl", fetchedAt: base, now: base.Add(5 * time.Minute), want: true},
		{name: "at ttl boundary is stale", fetchedAt: base, now: base.Add(ttl), want: false},
		{name: "past ttl", fetchedAt: base, now: base.Add(ttl + time.Second), want: false},
		{name: "way past ttl", fetchedAt: base, now: base.Add(24 * time.Hour), want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &cache.File{FetchedAt: tc.fetchedAt}
			if got := f.IsFresh(ttl, tc.now); got != tc.want {
				t.Errorf("IsFresh(%v, %v) with fetched_at=%v = %v, want %v",
					ttl, tc.now, tc.fetchedAt, got, tc.want)
			}
		})
	}
}

func TestFile_Age(t *testing.T) {
	fetched := time.Date(2026, 4, 18, 10, 0, 0, 0, time.UTC)
	now := fetched.Add(42 * time.Minute)
	f := &cache.File{FetchedAt: fetched}
	if got := f.Age(now); got != 42*time.Minute {
		t.Errorf("Age = %v, want %v", got, 42*time.Minute)
	}
}

func TestPath(t *testing.T) {
	cases := []struct {
		name     string
		xdg      string // "" means unset for this test
		home     string
		unsetXDG bool
		want     string
		wantErr  bool
	}{
		{
			name: "uses XDG_CACHE_HOME when absolute",
			xdg:  "/tmp/xdg-cache",
			home: "/home/user",
			want: "/tmp/xdg-cache/newsfetch/feed.json",
		},
		{
			name:     "falls back to $HOME/.cache when XDG unset",
			unsetXDG: true,
			home:     "/home/user",
			want:     "/home/user/.cache/newsfetch/feed.json",
		},
		{
			name: "falls back to $HOME/.cache when XDG is empty",
			xdg:  "",
			home: "/home/user",
			want: "/home/user/.cache/newsfetch/feed.json",
		},
		{
			name: "ignores relative XDG and falls back",
			xdg:  "relative/path",
			home: "/home/user",
			want: "/home/user/.cache/newsfetch/feed.json",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.unsetXDG {
				t.Setenv("XDG_CACHE_HOME", "x")
				os.Unsetenv("XDG_CACHE_HOME")
			} else {
				t.Setenv("XDG_CACHE_HOME", tc.xdg)
			}
			t.Setenv("HOME", tc.home)

			got, err := cache.Path()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Path: want error, got nil (got=%q)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Path: %v", err)
			}
			if got != tc.want {
				t.Errorf("Path = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRead_Errors(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		name    string
		setup   func(t *testing.T) string
		wantErr func(err error) bool
	}{
		{
			name: "missing file",
			setup: func(t *testing.T) string {
				return filepath.Join(dir, "missing.json")
			},
			wantErr: func(err error) bool { return errors.Is(err, os.ErrNotExist) },
		},
		{
			name: "corrupt json",
			setup: func(t *testing.T) string {
				p := filepath.Join(t.TempDir(), "bad.json")
				if err := os.WriteFile(p, []byte("{not valid json"), 0o644); err != nil {
					t.Fatalf("setup: %v", err)
				}
				return p
			},
			wantErr: func(err error) bool {
				return err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, cache.ErrSchemaVersion)
			},
		},
		{
			name: "unknown schema version",
			setup: func(t *testing.T) string {
				p := filepath.Join(t.TempDir(), "future.json")
				body := `{"version":99,"cached_by_version":"future","fetched_at":"2026-04-18T10:00:00Z","stories":[]}`
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatalf("setup: %v", err)
				}
				return p
			},
			wantErr: func(err error) bool { return errors.Is(err, cache.ErrSchemaVersion) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.setup(t)
			_, err := cache.Read(path)
			if err == nil {
				t.Fatalf("Read: want error, got nil")
			}
			if !tc.wantErr(err) {
				t.Errorf("Read: unexpected error shape: %v", err)
			}
		})
	}
}

func TestRead_RejectsV1Schema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "feed.json")
	v1 := []byte(`{"version": 1, "cached_by_version": "0.6.0", "fetched_at": "2026-08-01T00:00:00Z", "stories": []}`)
	if err := os.WriteFile(path, v1, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := cache.Read(path)
	if !errors.Is(err, cache.ErrSchemaVersion) {
		t.Errorf("Read(v1 cache) error = %v, want ErrSchemaVersion (v1 caches predate Summary/Feed and must refetch)", err)
	}
}

func TestWriteRead_RoundTripsSummaryAndFeed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "feed.json")
	in := &cache.File{
		Version: cache.SchemaVersion, CachedByVersion: "dev",
		FetchedAt: time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
		Stories: []fetch.Story{{
			ID: "rss-1", Title: "T", URL: "https://example.com/a", Source: "following",
			Summary: "a post about zig comptime", Feed: "https://example.com/feed.xml",
			CreatedAt: time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC), Tags: []string{},
		}},
	}
	if err := cache.Write(path, in); err != nil {
		t.Fatal(err)
	}
	got, err := cache.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stories[0].Summary != in.Stories[0].Summary || got.Stories[0].Feed != in.Stories[0].Feed {
		t.Errorf("round-trip lost fields: got %+v", got.Stories[0])
	}
}

func TestDir(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg-cache")
	t.Setenv("HOME", "/home/user")
	got, err := cache.Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if want := "/tmp/xdg-cache/newsfetch"; got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
}

// TestPoolPath walks both pools through both resolution paths, because the
// XDG-vs-$HOME fork is the part a per-pool refactor is most likely to
// duplicate and then get wrong for exactly one pool.
func TestPoolPath(t *testing.T) {
	cases := []struct {
		name     string
		xdg      string // "" means set-but-empty; see unsetXDG for genuinely unset
		unsetXDG bool
		home     string
		pool     string
		want     string
		wantErr  bool
	}{
		{
			name: "news under XDG_CACHE_HOME",
			xdg:  "/tmp/xdg-cache",
			home: "/home/user",
			pool: "news",
			want: "/tmp/xdg-cache/newsfetch/feed.json",
		},
		{
			name: "following under XDG_CACHE_HOME",
			xdg:  "/tmp/xdg-cache",
			home: "/home/user",
			pool: "following",
			want: "/tmp/xdg-cache/newsfetch/following.json",
		},
		{
			name:     "news falls back to $HOME/.cache",
			unsetXDG: true,
			home:     "/home/user",
			pool:     "news",
			want:     "/home/user/.cache/newsfetch/feed.json",
		},
		{
			name:     "following falls back to $HOME/.cache",
			unsetXDG: true,
			home:     "/home/user",
			pool:     "following",
			want:     "/home/user/.cache/newsfetch/following.json",
		},
		{
			name: "empty XDG falls back for both pools",
			xdg:  "",
			home: "/home/user",
			pool: "following",
			want: "/home/user/.cache/newsfetch/following.json",
		},
		{
			name: "relative XDG is ignored",
			xdg:  "relative/path",
			home: "/home/user",
			pool: "following",
			want: "/home/user/.cache/newsfetch/following.json",
		},
		{
			// A pool name with no cache file must be an error, never a
			// guessed filename: a typo that resolved to repos.json would
			// silently give the caller an empty cache forever.
			name:    "unknown pool is an error, not a guessed filename",
			xdg:     "/tmp/xdg-cache",
			home:    "/home/user",
			pool:    "repos",
			wantErr: true,
		},
		{
			name:    "empty pool name is an error",
			xdg:     "/tmp/xdg-cache",
			home:    "/home/user",
			pool:    "",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.unsetXDG {
				t.Setenv("XDG_CACHE_HOME", "x")
				os.Unsetenv("XDG_CACHE_HOME")
			} else {
				t.Setenv("XDG_CACHE_HOME", tc.xdg)
			}
			t.Setenv("HOME", tc.home)

			got, err := cache.PoolPath(tc.pool)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("PoolPath(%q): want error, got nil (got=%q)", tc.pool, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("PoolPath(%q): %v", tc.pool, err)
			}
			if got != tc.want {
				t.Errorf("PoolPath(%q) = %q, want %q", tc.pool, got, tc.want)
			}
		})
	}
}

// TestPath_IsStillTheNewsPoolFeedJSON is a regression pin, not a
// restatement of TestPath. feed.json is addressed by name from the
// statusline read path and from --uninstall, and every user already has
// one on disk; there is no migration. Path must therefore keep returning
// exactly PoolPath("news"), byte for byte.
func TestPath_IsStillTheNewsPoolFeedJSON(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg-cache")
	t.Setenv("HOME", "/home/user")
	got, err := cache.Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if want := "/tmp/xdg-cache/newsfetch/feed.json"; got != want {
		t.Errorf("Path = %q, want %q (renaming the news cache would strand every existing cache file)", got, want)
	}
	pooled, err := cache.PoolPath("news")
	if err != nil {
		t.Fatalf("PoolPath(news): %v", err)
	}
	if got != pooled {
		t.Errorf("Path = %q but PoolPath(\"news\") = %q; they must be the same path", got, pooled)
	}
}

// TestWriteRead_PoolsAreIndependentFiles proves the two pools address
// different files in one directory: writing the following pool must not
// disturb the news pool's cache.
func TestWriteRead_PoolsAreIndependentFiles(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	newsPath, err := cache.PoolPath("news")
	if err != nil {
		t.Fatal(err)
	}
	followingPath, err := cache.PoolPath("following")
	if err != nil {
		t.Fatal(err)
	}
	fetched := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	news := &cache.File{Version: cache.SchemaVersion, CachedByVersion: "dev", FetchedAt: fetched,
		Stories: []fetch.Story{{ID: "hn-1", Title: "News story", URL: "https://example.com/n", Source: "hackernews"}}}
	following := &cache.File{Version: cache.SchemaVersion, CachedByVersion: "dev", FetchedAt: fetched,
		Stories: []fetch.Story{{ID: "rss-1", Title: "Feed story", URL: "https://example.com/f", Source: "following", Feed: "https://example.com/feed.xml"}}}

	if err := cache.Write(newsPath, news); err != nil {
		t.Fatal(err)
	}
	if err := cache.Write(followingPath, following); err != nil {
		t.Fatal(err)
	}
	gotNews, err := cache.Read(newsPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotNews.Stories) != 1 || gotNews.Stories[0].Title != "News story" {
		t.Errorf("news cache = %+v, want the news story untouched by the following write", gotNews.Stories)
	}
	gotFollowing, err := cache.Read(followingPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotFollowing.Stories) != 1 || gotFollowing.Stories[0].Title != "Feed story" {
		t.Errorf("following cache = %+v, want the feed story", gotFollowing.Stories)
	}
}
