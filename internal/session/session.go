// Package session reads and writes the statusline pin store (sessions.json).
//
// The Claude Code statusline re-renders on every assistant message; without
// pinning, each re-render would reroll the story and the headline would
// flicker. The pin store maps an opaque key from the statusline stdin
// payload (prompt_id — one per user message) to the story selected for it,
// so re-renders within the same user turn are stable.
//
// The store lives under XDG_STATE_HOME next to the render history
// (internal/history): both record what was shown to the user, and keeping
// the state files together keeps backup/cleanup stories simple. Entry
// carries the rendered fields (title, URL) — not just the hash — so a pin
// hit renders without depending on the story still being in the cache,
// which refreshes on its own schedule.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/lockfile"
)

// SchemaVersion identifies the on-disk layout. Bump when Entry or File
// gains or loses a field, or when an existing field changes semantics.
const SchemaVersion = 1

// MaxEntries caps the pin store. Pin prunes to this many on every write,
// keeping the most recent. Each Claude Code user message adds one entry,
// so 64 covers a long working day; anything older has no live statusline
// still asking for it.
const MaxEntries = 64

// Entry is one pinned story.
type Entry struct {
	Key      string    `json:"key"`
	Hash     string    `json:"hash"`
	Title    string    `json:"title"`
	URL      string    `json:"url"`
	PinnedAt time.Time `json:"pinned_at"`
}

// File is the on-disk pin-store layout. JSON tags are part of the schema
// contract.
type File struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// ErrSchemaVersion is returned by [Read] when the file declares a schema
// version other than [SchemaVersion]. Callers should treat it like any
// other corruption and fall back to an empty store.
var ErrSchemaVersion = errors.New("session: unsupported schema version")

// Path returns the absolute path to sessions.json. It honours
// XDG_STATE_HOME first, then falls back to
// $HOME/.local/state/newsfetch/sessions.json.
func Path() (string, error) {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "newsfetch", "sessions.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve session path: %w", err)
	}
	return filepath.Join(home, ".local", "state", "newsfetch", "sessions.json"), nil
}

// Read parses the pin store at path. A missing file is not an error: it
// returns an empty File at the current SchemaVersion. Any other failure is
// returned to the caller, which should treat it as "no pins" rather than
// blocking the render.
func Read(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &File{Version: SchemaVersion}, nil
		}
		return nil, fmt.Errorf("read sessions: %w", err)
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse sessions: %w", err)
	}
	if f.Version != SchemaVersion {
		return nil, fmt.Errorf("%w: got %d, want %d", ErrSchemaVersion, f.Version, SchemaVersion)
	}
	return &f, nil
}

// Lookup returns the entry pinned under key.
func (f *File) Lookup(key string) (Entry, bool) {
	for _, e := range f.Entries {
		if e.Key == key {
			return e, true
		}
	}
	return Entry{}, false
}

// Pin persists e under e.Key, replacing any existing entry for the same
// key, pruned to the most recent [MaxEntries]. The write is atomic (temp
// file + rename) and the whole read-modify-write holds an exclusive
// advisory lock on a sidecar sessions.lock, mirroring history.Append:
// statusline renders run concurrently with terminal opens, so an
// unlocked RMW would silently lose pins the same way seen.json lost
// entries. A read failure (missing, corrupt, schema mismatch) is
// treated as an empty starting state — losing pins to corruption is
// better than refusing all subsequent writes.
func Pin(path string, e Entry) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create sessions dir: %w", err)
	}
	lock, err := lockfile.Acquire(filepath.Join(dir, "sessions.lock"), time.Second)
	if err != nil {
		return err
	}
	defer lock.Close() // close releases the flock

	f, err := Read(path)
	if err != nil {
		f = &File{Version: SchemaVersion}
	}
	kept := f.Entries[:0]
	for _, old := range f.Entries {
		if old.Key != e.Key {
			kept = append(kept, old)
		}
	}
	f.Entries = append(kept, e)
	if len(f.Entries) > MaxEntries {
		f.Entries = f.Entries[len(f.Entries)-MaxEntries:]
	}
	f.Version = SchemaVersion

	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("encode sessions: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "sessions-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp sessions: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp sessions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp sessions: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp sessions: %w", err)
	}
	return nil
}
