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
// edit the file manually before re-running --init.
var ErrConfigExists = errors.New("config file already exists")

// WriteConfig writes a TOML config file capturing the wizard's answers.
// Parent directories are created as needed. If path already exists,
// WriteConfig returns ErrConfigExists without touching the file.
//
// Only topics and style are emitted. cache_ttl_minutes and min_points are
// left unset so subsequent default changes flow through without the user
// having to edit their config.
func WriteConfig(path string, topics []string, style string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%w: %s", ErrConfigExists, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(renderConfigTOML(topics, style)), 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// renderConfigTOML produces the TOML body. Kept separate from WriteConfig
// for easy golden-style testing if that ever becomes useful.
func renderConfigTOML(topics []string, style string) string {
	var b strings.Builder
	b.WriteString("# newsfetch config. Edit freely; see spec.md for field meanings.\n\n")
	if len(topics) > 0 {
		b.WriteString("topics = [")
		for i, t := range topics {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(`"`)
			b.WriteString(tomlEscape(t))
			b.WriteString(`"`)
		}
		b.WriteString("]\n")
	} else {
		b.WriteString("topics = []\n")
	}
	fmt.Fprintf(&b, "style = %q\n", style)
	return b.String()
}

// tomlEscape escapes the minimal set of characters that can appear in a topic
// string. Topics are user-supplied so we can't assume they're alphanumeric.
func tomlEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
