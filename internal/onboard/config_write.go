package onboard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrConfigExists is returned by WriteConfig when the target path already
// exists. The wizard surfaces this as a refusal so the user can delete or
// edit the file manually before re-running --init. OverwriteConfig (used by
// --settings) does not raise it; --settings is the explicit edit-existing
// path.
var ErrConfigExists = errors.New("config file already exists")

// WriteConfig writes a TOML config file capturing the wizard's answers.
// Parent directories are created as needed. If path already exists,
// WriteConfig returns ErrConfigExists without touching the file.
//
// topics and style are always emitted. sources is emitted iff
// answers.Sources is non-nil — leaving it nil makes future default changes
// flow through to the user without requiring them to re-edit the file.
// cache_ttl_minutes and min_points are never emitted (same reason).
func WriteConfig(path string, answers Answers) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%w: %s", ErrConfigExists, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat config: %w", err)
	}
	return writeConfigBytes(path, answers)
}

// OverwriteConfig writes (or replaces) a TOML config file. Used by
// --settings, which is the explicit edit-existing-config path; refusing on
// existing files would defeat its purpose. Same field-emission rules as
// WriteConfig.
func OverwriteConfig(path string, answers Answers) error {
	return writeConfigBytes(path, answers)
}

// writeConfigBytes is the shared write-and-mkdir core for WriteConfig and
// OverwriteConfig. Pulled out so the existence check stays the only
// difference between the two public entry points.
func writeConfigBytes(path string, answers Answers) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(renderConfigTOML(answers)), 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// renderConfigTOML produces the TOML body. Kept separate from WriteConfig
// for easy golden-style testing if that ever becomes useful.
//
// count, ticker_marker, and ticker_boxed are emitted unconditionally (even
// when currently inert because style != "boxed" or count == 1) so a user's
// prior multi-story tuning survives a temporary switch away. This mirrors
// the wizard's hide-don't-clear behaviour for the same fields.
func renderConfigTOML(a Answers) string {
	var b strings.Builder
	b.WriteString("# newsfetch config. Edit freely; see spec.md for field meanings.\n\n")
	b.WriteString(renderStringArray("topics", a.Topics))
	fmt.Fprintf(&b, "style = %q\n", a.Style)
	if a.Sources != nil {
		b.WriteString(renderStringArray("sources", a.Sources))
	}
	fmt.Fprintf(&b, "count = %d\n", a.Count)
	fmt.Fprintf(&b, "ticker_marker = %q\n", a.TickerMarker)
	fmt.Fprintf(&b, "ticker_boxed = %t\n", a.TickerBoxed)
	return b.String()
}

// renderStringArray emits one TOML key = ["a", "b"] line, with [] for empty.
// Strings are escaped via tomlEscape since topics are user-supplied.
func renderStringArray(key string, vals []string) string {
	var b strings.Builder
	if len(vals) == 0 {
		fmt.Fprintf(&b, "%s = []\n", key)
		return b.String()
	}
	fmt.Fprintf(&b, "%s = [", key)
	for i, v := range vals {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(`"`)
		b.WriteString(tomlEscape(v))
		b.WriteString(`"`)
	}
	b.WriteString("]\n")
	return b.String()
}

// tomlEscape escapes the minimal set of characters that can appear in a
// user-supplied string (topic, source name).
func tomlEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
