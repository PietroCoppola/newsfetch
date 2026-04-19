// Package refreshlog persists diagnostic messages from the detached
// refresh child. The parent redirects the child's stdout/stderr to
// /dev/null so without this log its errors would vanish and users
// could not diagnose a stale cache.
//
// Writes use O_APPEND; single-line small payloads are POSIX-atomic in
// practice. Rotation is best-effort under concurrency — losing one
// diagnostic entry when two children race is acceptable given the log's
// purpose. Successful refreshes never touch this file.
package refreshlog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	filename   = "refresh.log"
	maxEntries = 20
	// rotateAt is the file-size threshold that triggers rotation. 4KiB
	// holds ~40 normal lines, well above maxEntries, so rotation is
	// rare and non-invasive under typical usage.
	rotateAt = 4 << 10
)

// Path returns the absolute path to the refresh log. Mirrors
// cache.Path()'s XDG resolution exactly so the two files co-locate.
func Path() (string, error) {
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "newsfetch", filename), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve refresh log path: %w", err)
	}
	return filepath.Join(home, ".cache", "newsfetch", filename), nil
}

// Append adds one line to the refresh log with an ISO-8601 timestamp
// and ERROR level. Embedded newlines in msg are flattened to spaces so
// one call always produces one line. Callers should not inspect the
// returned error — the log is best-effort, refresh failure reporting
// is not worth propagating further.
func Append(msg string) error {
	path, err := Path()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open refresh log: %w", err)
	}
	defer f.Close()
	line := fmt.Sprintf("%s ERROR %s\n",
		time.Now().UTC().Format(time.RFC3339),
		strings.ReplaceAll(msg, "\n", " "))
	if _, err := f.WriteString(line); err != nil {
		return fmt.Errorf("write refresh log: %w", err)
	}
	info, err := f.Stat()
	if err == nil && info.Size() > rotateAt {
		_ = rotate(path)
	}
	return nil
}

func rotate(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) <= maxEntries {
		return nil
	}
	tail := lines[len(lines)-maxEntries:]
	return os.WriteFile(path, []byte(strings.Join(tail, "\n")+"\n"), 0o644)
}
