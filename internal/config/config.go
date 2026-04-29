// Package config loads and validates the user's newsfetch configuration.
//
// The config file is optional: a missing file resolves to Defaults() with no
// warning. A malformed file causes Load to return an error and the caller is
// responsible for emitting a one-line warning to stderr and proceeding with
// Defaults(). See the design spec §4.1.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/PietroCoppola/newsfetch/internal/defaults"
)

// Config is the merged runtime settings: compile-time defaults overlaid with
// values from the TOML file and any applicable CLI overrides.
type Config struct {
	Topics    []string      // nil → no topic filter
	Style     string        // "boxed" | "minimal" | "json"
	CacheTTL  time.Duration // derived from cache_ttl_minutes
	MinPoints int
	// Sources lists which Source implementations to fetch from, by name.
	// Names must appear in fetch.KnownSourceNames; Validate drops unknowns
	// and falls back to defaults if nothing valid remains. M4 default is
	// ["hackernews"] — Lobste.rs is opt-in via config.
	Sources []string
	// Count is the number of stories rendered per invocation, 1..MaxCount.
	// Values outside the range are clamped by Validate.
	Count int
	// TickerMarker is the symbol prefixing each non-hero entry in
	// multi-story renders. Names match render.KnownTickerMarkers.
	TickerMarker string
	// TickerBoxed selects between one connected box (true) and a hero box
	// with plain ticker lines beneath (false). Only takes effect when
	// Style == "boxed" and Count > 1.
	TickerBoxed bool
}

// Defaults returns the compile-time fallback config. Validate is a no-op on
// this value by construction.
func Defaults() Config {
	return Config{
		Topics:       nil,
		Style:        defaults.Style,
		CacheTTL:     defaults.CacheTTL,
		MinPoints:    defaults.MinPoints,
		Sources:      append([]string(nil), defaults.Sources...),
		Count:        defaults.Count,
		TickerMarker: defaults.TickerMarker,
		TickerBoxed:  defaults.TickerBoxed,
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

// Load reads and parses the config file at path. It returns:
//
//   - (Defaults(), nil) if the file does not exist (normal first-run case).
//   - (Defaults(), err) if the file exists but fails to parse. The caller
//     is responsible for emitting a warning and proceeding with Defaults().
//   - (merged, nil) where merged is Defaults() overlaid with the fields
//     actually present in the file. Unknown keys are silently ignored.
//
// Integer fields present in the file always override (including zero), so
// Validate can see and correct intentionally out-of-range values. Missing
// fields keep their default.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Defaults(), nil
		}
		return Defaults(), fmt.Errorf("read config: %w", err)
	}
	var raw struct {
		Topics          []string `toml:"topics"`
		Style           string   `toml:"style"`
		CacheTTLMinutes int      `toml:"cache_ttl_minutes"`
		MinPoints       int      `toml:"min_points"`
		Sources         []string `toml:"sources"`
		Count           int      `toml:"count"`
		TickerMarker    string   `toml:"ticker_marker"`
		TickerBoxed     bool     `toml:"ticker_boxed"`
	}
	meta, err := toml.Decode(string(data), &raw)
	if err != nil {
		return Defaults(), fmt.Errorf("parse config: %w", err)
	}
	cfg := Defaults()
	if meta.IsDefined("topics") {
		cfg.Topics = raw.Topics
	}
	if meta.IsDefined("style") {
		cfg.Style = raw.Style
	}
	if meta.IsDefined("cache_ttl_minutes") {
		cfg.CacheTTL = time.Duration(raw.CacheTTLMinutes) * time.Minute
	}
	if meta.IsDefined("min_points") {
		cfg.MinPoints = raw.MinPoints
	}
	if meta.IsDefined("sources") {
		cfg.Sources = raw.Sources
	}
	if meta.IsDefined("count") {
		cfg.Count = raw.Count
	}
	if meta.IsDefined("ticker_marker") {
		cfg.TickerMarker = raw.TickerMarker
	}
	if meta.IsDefined("ticker_boxed") {
		cfg.TickerBoxed = raw.TickerBoxed
	}
	return cfg, nil
}
