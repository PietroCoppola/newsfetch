// Package cache reads and writes the newsfetch story cache (feed.json).
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
	"time"

	"github.com/PietroCoppola/newsfetch/internal/fetch"
)

// SchemaVersion identifies the on-disk layout. Bump it when File gains or
// loses a field, or when an existing field changes semantics.
const SchemaVersion = 1

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

// Path returns the absolute path to feed.json. It honours XDG_CACHE_HOME
// first, then falls back to $HOME/.cache/newsfetch/feed.json. It returns an
// error if neither is resolvable.
func Path() (string, error) {
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "newsfetch", "feed.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve cache path: %w", err)
	}
	return filepath.Join(home, ".cache", "newsfetch", "feed.json"), nil
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
// never leaves a half-written cache. The caller is responsible for setting
// f.Version to [SchemaVersion] and f.CachedByVersion to the current binary
// version.
func Write(path string, f *File) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cache: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "feed-*.json.tmp")
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
