// Package config loads and validates the user's newsfetch configuration.
//
// The config file is optional: a missing file resolves to Defaults() with no
// warning. A malformed file causes Load to return an error and the caller is
// responsible for emitting a one-line warning to stderr and proceeding with
// Defaults(). See the design spec §4.1.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/defaults"
)

// Config is the merged runtime settings: compile-time defaults overlaid with
// values from the TOML file and any applicable CLI overrides.
type Config struct {
	Topics    []string      // nil → no topic filter
	Style     string        // "boxed" | "minimal" | "json"
	CacheTTL  time.Duration // derived from cache_ttl_minutes
	MinPoints int
}

// Defaults returns the compile-time fallback config. Validate is a no-op on
// this value by construction.
func Defaults() Config {
	return Config{
		Topics:    nil,
		Style:     defaults.Style,
		CacheTTL:  defaults.CacheTTL,
		MinPoints: defaults.MinPoints,
	}
}

// Path returns the absolute path to the config file. XDG_CONFIG_HOME is
// honoured only when it is an absolute path; otherwise it falls back to
// $HOME/.config/newsfetch/config.toml. Mirrors the M1 cache.Path() contract.
func Path() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "newsfetch", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve config path: %w", err)
	}
	return filepath.Join(home, ".config", "newsfetch", "config.toml"), nil
}
