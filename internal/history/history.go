// Package history reads and writes the newsfetch render history (seen.json).
//
// The history file records stories that have already been shown to the user,
// so the ranker can pre-filter them out and avoid surfacing the same item
// twice. It is also the durable backing store for the planned browse
// subcommand's history view.
//
// Unlike the story cache (internal/cache), history is irreplaceable user
// state, not a rebuildable derived artefact. It therefore lives under
// XDG_STATE_HOME rather than XDG_CACHE_HOME — losing seen.json loses the
// dedup memory, not just a transient pool that the next fetch can repopulate.
//
// On the hot render path the file is read once, converted to a hash set, and
// then appended to (a single Append per render). The 500-entry cap is
// enforced at write time by keeping only the most recent entries.
package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SchemaVersion identifies the on-disk layout. Bump when Entry or File gains
// or loses a field, or when an existing field changes semantics.
const SchemaVersion = 1

// MaxEntries caps the number of entries persisted in seen.json. Append prunes
// to this many on every write, keeping the most-recently-rendered entries.
// Bounding disk usage matters more than retaining ancient history — the M7
// browse view will further filter for display purposes.
const MaxEntries = 500

// Entry is one rendered story. The schema is intentionally rich enough that
// the planned browse subcommand's history view can render entries without
// needing to re-fetch from the network.
type Entry struct {
	Hash       string    `json:"hash"`
	Title      string    `json:"title"`
	URL        string    `json:"url"`
	Source     string    `json:"source"`
	Tags       []string  `json:"tags"`
	RenderedAt time.Time `json:"rendered_at"`
}

// File is the on-disk history layout. JSON tags are part of the schema
// contract.
type File struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// ErrSchemaVersion is returned by [Read] when the history file declares a
// schema version other than [SchemaVersion]. Callers should treat this the
// same as any other corruption error and fall back to an empty history.
var ErrSchemaVersion = errors.New("history: unsupported schema version")

// Path returns the absolute path to seen.json. It honours XDG_STATE_HOME
// first, then falls back to $HOME/.local/state/newsfetch/seen.json. It
// returns an error if neither is resolvable.
func Path() (string, error) {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "newsfetch", "seen.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve history path: %w", err)
	}
	return filepath.Join(home, ".local", "state", "newsfetch", "seen.json"), nil
}

// Read parses the history file at path. A missing file is not an error: it
// returns an empty File with the current SchemaVersion. Any other read or
// parse failure is returned to the caller, which should treat it as
// equivalent to "no history" rather than blocking the render.
func Read(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &File{Version: SchemaVersion}, nil
		}
		return nil, fmt.Errorf("read history: %w", err)
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse history: %w", err)
	}
	if f.Version != SchemaVersion {
		return nil, fmt.Errorf("%w: got %d, want %d", ErrSchemaVersion, f.Version, SchemaVersion)
	}
	return &f, nil
}

// Append adds entries to the history at path and persists the result, pruned
// to the most recent [MaxEntries]. The write is atomic (temp file + rename)
// so a killed process cannot leave a half-written history. A missing file is
// treated as an empty starting state.
//
// Pruning keeps the tail of the slice — callers therefore must append in
// render order (oldest first within the batch).
func Append(path string, entries []Entry) error {
	f, err := Read(path)
	if err != nil {
		// Treat any read failure (corrupt, schema mismatch) as starting
		// from empty. Losing history to a transient corruption is better
		// than refusing all subsequent writes.
		f = &File{Version: SchemaVersion}
	}
	f.Entries = append(f.Entries, entries...)
	if len(f.Entries) > MaxEntries {
		f.Entries = f.Entries[len(f.Entries)-MaxEntries:]
	}
	f.Version = SchemaVersion

	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("encode history: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create history dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "seen-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp history: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp history: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp history: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp history: %w", err)
	}
	return nil
}

// HashSet returns the entry hashes as a set for O(1) pre-filter lookups.
func (f *File) HashSet() map[string]struct{} {
	out := make(map[string]struct{}, len(f.Entries))
	for _, e := range f.Entries {
		out[e.Hash] = struct{}{}
	}
	return out
}
