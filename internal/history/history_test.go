package history_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/history"
)

func TestRead_MissingFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "absent.json")

	f, err := history.Read(path)
	if err != nil {
		t.Fatalf("Read on missing file: %v", err)
	}
	if f.Version != history.SchemaVersion {
		t.Errorf("Version = %d, want %d", f.Version, history.SchemaVersion)
	}
	if len(f.Entries) != 0 {
		t.Errorf("Entries = %v, want empty", f.Entries)
	}
}

func TestAppend_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seen.json")

	entries := []history.Entry{
		{
			Hash:       "hash-a",
			Title:      "Story A",
			URL:        "https://example.com/a",
			Source:     "hackernews",
			Tags:       []string{"rust"},
			RenderedAt: time.Date(2026, 4, 27, 9, 0, 0, 0, time.UTC),
		},
		{
			Hash:       "hash-b",
			Title:      "Story B",
			URL:        "https://example.com/b",
			Source:     "lobsters",
			Tags:       []string{"go", "performance"},
			RenderedAt: time.Date(2026, 4, 27, 10, 0, 0, 0, time.UTC),
		},
	}

	if err := history.Append(path, entries); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := history.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("Entries len = %d, want 2", len(got.Entries))
	}
	if got.Entries[0].Hash != "hash-a" || got.Entries[1].Hash != "hash-b" {
		t.Errorf("order not preserved: %v", got.Entries)
	}
	if !got.Entries[1].RenderedAt.Equal(entries[1].RenderedAt) {
		t.Errorf("timestamp mismatch: %v vs %v", got.Entries[1].RenderedAt, entries[1].RenderedAt)
	}
}

func TestAppend_PrunesToMaxEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seen.json")

	// Seed with 400 entries.
	first := make([]history.Entry, 400)
	for i := range first {
		first[i] = history.Entry{Hash: fmtHash("first", i), RenderedAt: time.Unix(int64(i), 0)}
	}
	if err := history.Append(path, first); err != nil {
		t.Fatalf("seed Append: %v", err)
	}

	// Append another 200, total would be 600 -> pruned to 500.
	second := make([]history.Entry, 200)
	for i := range second {
		second[i] = history.Entry{Hash: fmtHash("second", i), RenderedAt: time.Unix(int64(1000+i), 0)}
	}
	if err := history.Append(path, second); err != nil {
		t.Fatalf("second Append: %v", err)
	}

	got, err := history.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got.Entries) != history.MaxEntries {
		t.Fatalf("len = %d, want %d", len(got.Entries), history.MaxEntries)
	}
	// Tail is preserved: last entry should be the last of the second batch.
	wantTail := fmtHash("second", 199)
	if got.Entries[len(got.Entries)-1].Hash != wantTail {
		t.Errorf("tail hash = %q, want %q", got.Entries[len(got.Entries)-1].Hash, wantTail)
	}
	// Oldest first-batch entries should be gone: 600 total - 500 cap = 100
	// dropped from the head, so first-batch index 99 is the oldest survivor.
	wantHead := fmtHash("first", 100)
	if got.Entries[0].Hash != wantHead {
		t.Errorf("head hash = %q, want %q", got.Entries[0].Hash, wantHead)
	}
}

func TestHashSet(t *testing.T) {
	f := &history.File{
		Entries: []history.Entry{
			{Hash: "a"}, {Hash: "b"}, {Hash: "c"},
		},
	}
	set := f.HashSet()
	if len(set) != 3 {
		t.Fatalf("set len = %d, want 3", len(set))
	}
	for _, h := range []string{"a", "b", "c"} {
		if _, ok := set[h]; !ok {
			t.Errorf("missing hash %q", h)
		}
	}
	if _, ok := set["missing"]; ok {
		t.Error("set contains hash that wasn't appended")
	}
}

func TestPath(t *testing.T) {
	cases := []struct {
		name     string
		xdg      string
		home     string
		unsetXDG bool
		want     string
	}{
		{
			name: "uses XDG_STATE_HOME when absolute",
			xdg:  "/tmp/xdg-state",
			home: "/home/user",
			want: "/tmp/xdg-state/newsfetch/seen.json",
		},
		{
			name:     "falls back to $HOME/.local/state when XDG unset",
			unsetXDG: true,
			home:     "/home/user",
			want:     "/home/user/.local/state/newsfetch/seen.json",
		},
		{
			name: "falls back when XDG empty",
			xdg:  "",
			home: "/home/user",
			want: "/home/user/.local/state/newsfetch/seen.json",
		},
		{
			name: "ignores relative XDG and falls back",
			xdg:  "relative/path",
			home: "/home/user",
			want: "/home/user/.local/state/newsfetch/seen.json",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.unsetXDG {
				t.Setenv("XDG_STATE_HOME", "x")
				os.Unsetenv("XDG_STATE_HOME")
			} else {
				t.Setenv("XDG_STATE_HOME", tc.xdg)
			}
			t.Setenv("HOME", tc.home)

			got, err := history.Path()
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
	cases := []struct {
		name    string
		setup   func(t *testing.T) string
		wantErr func(err error) bool
	}{
		{
			name: "corrupt json returns error",
			setup: func(t *testing.T) string {
				p := filepath.Join(t.TempDir(), "bad.json")
				if err := os.WriteFile(p, []byte("{not valid"), 0o644); err != nil {
					t.Fatalf("setup: %v", err)
				}
				return p
			},
			wantErr: func(err error) bool {
				return err != nil && !errors.Is(err, history.ErrSchemaVersion)
			},
		},
		{
			name: "unknown schema version",
			setup: func(t *testing.T) string {
				p := filepath.Join(t.TempDir(), "future.json")
				body := `{"version":99,"entries":[]}`
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatalf("setup: %v", err)
				}
				return p
			},
			wantErr: func(err error) bool { return errors.Is(err, history.ErrSchemaVersion) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.setup(t)
			_, err := history.Read(path)
			if err == nil {
				t.Fatalf("Read: want error, got nil")
			}
			if !tc.wantErr(err) {
				t.Errorf("unexpected error shape: %v", err)
			}
		})
	}
}

func TestAppend_RecoversFromCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seen.json")

	if err := os.WriteFile(path, []byte("garbage"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	entries := []history.Entry{{Hash: "fresh"}}
	if err := history.Append(path, entries); err != nil {
		t.Fatalf("Append on corrupt file: %v", err)
	}
	got, err := history.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Hash != "fresh" {
		t.Errorf("recovered entries = %v, want [fresh]", got.Entries)
	}
}

func fmtHash(prefix string, i int) string {
	return prefix + "-" + strconv.Itoa(i)
}
