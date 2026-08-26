package session_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

// TestGetOrCreate_ConcurrentSameKeyCreatesOnce is the one-story-per-turn
// invariant: concurrent statusline renders sharing a prompt_id must all
// see the same pinned story. An unserialized lookup-then-create lets every
// caller miss, select its own story, and race to persist it.
func TestGetOrCreate_ConcurrentSameKeyCreatesOnce(t *testing.T) {
	path := tmpPath(t)
	const goroutines = 20
	var creates atomic.Int64
	titles := make([]string, goroutines)
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e, err := session.GetOrCreate(path, "prompt-1", func() (session.Entry, error) {
				n := creates.Add(1)
				return entry("prompt-1", fmt.Sprintf("Story %d", n)), nil
			})
			titles[i], errs[i] = e.Title, err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: GetOrCreate error: %v", i, err)
		}
	}
	if got := creates.Load(); got != 1 {
		t.Errorf("create called %d times, want 1", got)
	}
	for i, got := range titles {
		if got != titles[0] {
			t.Errorf("goroutine %d saw %q, goroutine 0 saw %q — pins diverged", i, got, titles[0])
		}
	}
	if titles[0] == "" {
		t.Error("every goroutine got an empty entry")
	}
	f, err := session.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != 1 {
		t.Errorf("entries = %d, want 1", len(f.Entries))
	}
}

func TestGetOrCreate_CreateErrorPersistsNothing(t *testing.T) {
	path := tmpPath(t)
	boom := errors.New("no cached stories")
	if _, err := session.GetOrCreate(path, "prompt-1", func() (session.Entry, error) {
		return session.Entry{}, boom
	}); !errors.Is(err, boom) {
		t.Fatalf("GetOrCreate error = %v, want one wrapping %v", err, boom)
	}
	f, err := session.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != 0 {
		t.Errorf("entries = %d after a failed create, want 0", len(f.Entries))
	}

	got, err := session.GetOrCreate(path, "prompt-1", func() (session.Entry, error) {
		return entry("prompt-1", "Recovered"), nil
	})
	if err != nil {
		t.Fatalf("GetOrCreate after a failed create: %v", err)
	}
	if got.Title != "Recovered" {
		t.Errorf("entry = %q, want %q", got.Title, "Recovered")
	}
	f, err = session.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := f.Lookup("prompt-1"); !ok || e.Title != "Recovered" {
		t.Errorf("store holds %+v (found=%t), want the recovered entry", e, ok)
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
