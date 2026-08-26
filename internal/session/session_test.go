package session_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/session"
)

func tmpPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "sessions.json")
}

func entry(key, title string) session.Entry {
	return session.Entry{
		Key: key, Hash: "example.com/" + key, Title: title,
		URL: "https://example.com/" + key, PinnedAt: time.Now().UTC(),
	}
}

func TestPath_HonoursXDGStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-state")
	got, err := session.Path()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/tmp/xdg-state", "newsfetch", "sessions.json")
	if got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestPath_RelativeXDGFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "relative/path")
	got, err := session.Path()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, filepath.Join(".local", "state", "newsfetch", "sessions.json")) {
		t.Errorf("Path() = %q, want $HOME/.local/state fallback", got)
	}
}

func TestRead_MissingFileIsEmpty(t *testing.T) {
	f, err := session.Read(tmpPath(t))
	if err != nil {
		t.Fatalf("Read(missing) error: %v", err)
	}
	if len(f.Entries) != 0 || f.Version != session.SchemaVersion {
		t.Errorf("Read(missing) = %+v, want empty file at current schema", f)
	}
}

func TestRead_CorruptFileErrors(t *testing.T) {
	path := tmpPath(t)
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Read(path); err == nil {
		t.Error("Read(corrupt) = nil error, want parse error")
	}
}

func TestPin_RoundTripAndLookup(t *testing.T) {
	path := tmpPath(t)
	e := entry("prompt-1", "First story")
	if err := session.Pin(path, e); err != nil {
		t.Fatal(err)
	}
	f, err := session.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := f.Lookup("prompt-1")
	if !ok {
		t.Fatal("Lookup(prompt-1) not found after Pin")
	}
	if got.Title != "First story" || got.URL != e.URL || got.Hash != e.Hash {
		t.Errorf("Lookup = %+v, want %+v", got, e)
	}
	if _, ok := f.Lookup("prompt-2"); ok {
		t.Error("Lookup(prompt-2) found, want miss")
	}
}

func TestPin_ReplacesExistingKey(t *testing.T) {
	path := tmpPath(t)
	if err := session.Pin(path, entry("prompt-1", "Old")); err != nil {
		t.Fatal(err)
	}
	if err := session.Pin(path, entry("prompt-1", "New")); err != nil {
		t.Fatal(err)
	}
	f, err := session.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != 1 {
		t.Fatalf("entries = %d, want 1 (replace, not append)", len(f.Entries))
	}
	if got, _ := f.Lookup("prompt-1"); got.Title != "New" {
		t.Errorf("Lookup = %q, want %q", got.Title, "New")
	}
}

func TestPin_PrunesToMaxEntriesKeepingRecent(t *testing.T) {
	path := tmpPath(t)
	for i := 0; i < session.MaxEntries+10; i++ {
		if err := session.Pin(path, entry(fmt.Sprintf("k%d", i), "t")); err != nil {
			t.Fatal(err)
		}
	}
	f, err := session.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != session.MaxEntries {
		t.Fatalf("entries = %d, want %d", len(f.Entries), session.MaxEntries)
	}
	if _, ok := f.Lookup("k0"); ok {
		t.Error("oldest entry survived prune")
	}
	if _, ok := f.Lookup(fmt.Sprintf("k%d", session.MaxEntries+9)); !ok {
		t.Error("newest entry pruned")
	}
}

func TestPin_CorruptFileStartsEmpty(t *testing.T) {
	path := tmpPath(t)
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := session.Pin(path, entry("prompt-1", "t")); err != nil {
		t.Fatalf("Pin over corrupt file: %v", err)
	}
	f, err := session.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != 1 {
		t.Errorf("entries = %d, want 1", len(f.Entries))
	}
}
