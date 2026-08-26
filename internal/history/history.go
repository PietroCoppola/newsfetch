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
	"syscall"
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
// so a killed process cannot leave a half-written history, and the whole
// read-modify-write holds an exclusive advisory lock on a sidecar
// seen.lock file: concurrent renders (a tmux or iTerm session restore
// opens many terminals within milliseconds) would otherwise base their
// write on the same snapshot and the last rename would silently drop the
// others' entries. The kernel releases the lock when the process exits —
// crashed included — so there is no stale-lock recovery to handle. A
// missing file is treated as an empty starting state.
//
// Pruning keeps the tail of the slice — callers therefore must append in
// render order (oldest first within the batch).
func Append(path string, entries []Entry) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create history dir: %w", err)
	}
	lock, err := acquireLock(filepath.Join(dir, "seen.lock"), lockTimeout)
	if err != nil {
		return err
	}
	defer lock.Close() // close releases the flock

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

// lockTimeout bounds how long Append waits for the sidecar lock. The
// critical section is ~1ms, so even a terminal-restore burst of
// contenders clears in well under this; a holder stuck longer (stopped
// process, hung disk I/O) forfeits this render's history entry instead of
// hanging the terminal open — losing one entry matters less than the
// user's shell prompt appearing.
const lockTimeout = time.Second

// acquireLock takes an exclusive advisory lock on path, waiting at most
// timeout for a holder to release it. Two flock realities shape the loop:
// a blocking LOCK_EX can return EINTR when a signal lands (Go's
// async-preemption SIGURG makes that routine, which is why cmd/go's
// filelock retries it), so the non-blocking form is polled instead; and
// the kernel drops the lock when the file closes — crashed holders
// included — so callers release by closing the returned file and no
// stale-lock recovery exists.
func acquireLock(path string, timeout time.Duration) (*os.File, error) {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open history lock: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return lock, nil
		case errors.Is(err, syscall.EINTR):
			// Interrupted before the attempt resolved; try again now.
		case errors.Is(err, syscall.EWOULDBLOCK):
			if time.Now().After(deadline) {
				lock.Close()
				return nil, fmt.Errorf("lock history: held elsewhere for over %s", timeout)
			}
			time.Sleep(2 * time.Millisecond)
		default:
			lock.Close()
			return nil, fmt.Errorf("lock history: %w", err)
		}
	}
}

// HashSet returns the entry hashes as a set for O(1) pre-filter lookups.
// Includes every entry regardless of age. For time-gated dedup, prefer
// [File.RecentHashSet].
func (f *File) HashSet() map[string]struct{} {
	out := make(map[string]struct{}, len(f.Entries))
	for _, e := range f.Entries {
		out[e.Hash] = struct{}{}
	}
	return out
}

// RecentHashSet returns the hashes of entries rendered within window of
// now. Older entries age out of the dedup pool and become eligible for
// re-rendering. A non-positive window returns an empty set — callers
// wanting "no time gate" should treat that as "history dedup disabled"
// rather than asking for a window of zero, which would gate everything
// out.
func (f *File) RecentHashSet(now time.Time, window time.Duration) map[string]struct{} {
	if window <= 0 {
		return map[string]struct{}{}
	}
	cutoff := now.Add(-window)
	out := make(map[string]struct{}, len(f.Entries))
	for _, e := range f.Entries {
		if e.RenderedAt.After(cutoff) {
			out[e.Hash] = struct{}{}
		}
	}
	return out
}
