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
