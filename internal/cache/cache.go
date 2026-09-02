// Package cache reads and writes newsfetch's story caches.
//
// Each render pool owns a file in one directory: the news pool keeps
// feed.json — unchanged from before pools existed, because the statusline
// read path, --uninstall, and every cache already on disk address it by
// that name — and the following pool caches to following.json beside it.
//
// The cache is on the hot render path: every invocation reads it, most
// invocations only read it. Writes happen off the hot path from the
// background refresh, so the design optimises for read simplicity and for
// recovering cleanly from a torn or missing file.
package cache

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/fetch"
)

// SchemaVersion identifies the on-disk layout. Bump it when File gains or
// loses a field, or when an existing field changes semantics.
const SchemaVersion = 2

// File is the on-disk cache layout. JSON tags are part of the schema
// contract.
type File struct {
	Version         int           `json:"version"`
	CachedByVersion string        `json:"cached_by_version"`
	FetchedAt       time.Time     `json:"fetched_at"`
	Stories         []fetch.Story `json:"stories"`
}

// ErrSchemaVersion is returned by [Read] when the cache file declares a
// schema version other than [SchemaVersion]. Callers can treat it the same
// as any other cache-corruption error.
var ErrSchemaVersion = errors.New("cache: unsupported schema version")

// Dir returns the absolute path to newsfetch's cache directory, honouring
// XDG_CACHE_HOME first and falling back to $HOME/.cache/newsfetch. Pool
// files live side by side in it, and callers that need the directory
// itself (a refresh writing two pools, an uninstall listing what it will
// remove) should ask for it here rather than taking filepath.Dir of a
// pool path, which would silently depend on that pool existing.
func Dir() (string, error) {
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "newsfetch"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve cache dir: %w", err)
	}
	return filepath.Join(home, ".cache", "newsfetch"), nil
}

// poolFile maps a pool name to its cache file's basename. The mapping is
// deliberately a closed switch rather than a lookup that falls through to
// a derived name: an unrecognised pool has to be an error, because a
// guessed filename would hand the caller a permanently empty cache and
// nothing downstream would ever notice.
//
// news keeps feed.json for compatibility, so the mapping is not
// mechanical and cannot be replaced by pool+".json".
func poolFile(pool string) (string, bool) {
	switch pool {
	case "news":
		return "feed.json", true
	case "following":
		return "following.json", true
	}
	return "", false
}

// PoolPath returns the absolute path to pool's cache file. It returns an
// error for any pool with no cache file of its own.
func PoolPath(pool string) (string, error) {
	name, ok := poolFile(pool)
	if !ok {
		return "", fmt.Errorf("cache path: unknown pool %q", pool)
	}
	dir, err := Dir()
	if err != nil {
		return "", fmt.Errorf("cache path for pool %q: %w", pool, err)
	}
	return filepath.Join(dir, name), nil
}

// Path returns the absolute path to the news pool's cache, feed.json. It
// is exactly PoolPath("news") and exists because callers reach for the
// news cache by function value (see cmd/newsfetch's refresh wiring) and
// because renaming that file would strand every cache already on disk.
func Path() (string, error) {
	return PoolPath("news")
}

// Read parses the cache at path. It returns an error if the file is missing,
// unreadable, not valid JSON, or declares a schema version other than
// [SchemaVersion].
func Read(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read cache: %w", err)
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse cache: %w", err)
	}
	if f.Version != SchemaVersion {
		return nil, fmt.Errorf("%w: got %d, want %d", ErrSchemaVersion, f.Version, SchemaVersion)
	}
	return &f, nil
}

// Write persists f to path using a temp file + rename so a killed process
// never leaves a half-written cache. The temp file is named after the
// target (see [tempPattern]) so debris from a crashed write names the pool
// that actually crashed. The caller is responsible for setting f.Version
// to [SchemaVersion] and f.CachedByVersion to the current binary version.
func Write(path string, f *File) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cache: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, tempPattern(path))
	if err != nil {
		return fmt.Errorf("create temp cache: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp cache: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp cache: %w", err)
	}
	return nil
}

// tempPattern builds the os.CreateTemp pattern for a write to path: the
// target's own basename, minus .json, plus the random-suffix marker. The
// pattern was hardcoded to feed-*.json.tmp when feed.json was the only
// cache; now that every pool has a file in the same directory, debris
// from an interrupted write has to name the pool it came from, or a
// reader of that directory (or a future cleanup that pattern-matches)
// blames the wrong pool.
//
// It stays "*.json.tmp" rather than "*.tmp": the content is JSON, and the
// suffix is what distinguishes a torn write from a live cache to anything
// globbing the directory.
func tempPattern(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".json") + "-*.json.tmp"
}

// Age returns how long ago FetchedAt was relative to now.
func (f *File) Age(now time.Time) time.Duration {
	return now.Sub(f.FetchedAt)
}

// IsFresh reports whether the cache is within ttl of now. The TTL boundary
// itself counts as stale - a file exactly ttl old will not render without a
// refresh.
func (f *File) IsFresh(ttl time.Duration, now time.Time) bool {
	return f.Age(now) < ttl
}
