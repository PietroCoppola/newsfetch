package refreshlog_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PietroCoppola/newsfetch/internal/refreshlog"
)

func TestAppend_CreatesDirAndFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)

	if err := refreshlog.Append("boom"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	path := filepath.Join(dir, "newsfetch", "refresh.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), "ERROR boom") {
		t.Errorf("log missing expected content: %q", data)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Errorf("log entry should end with newline: %q", data)
	}
}

func TestAppend_Rotates(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)

	// Write a fat line repeatedly until rotation triggers. A single
	// entry ~200B; 50 entries ~10KB, well past the 4KiB rotation
	// threshold. After rotation the file should have ≤ 20 lines.
	big := strings.Repeat("x", 180)
	for i := 0; i < 50; i++ {
		if err := refreshlog.Append(big); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	path := filepath.Join(dir, "newsfetch", "refresh.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) > 20 {
		t.Errorf("log has %d lines after rotation; want ≤ 20", len(lines))
	}
	// Content should be the tail (contains the big string, not nothing).
	if len(lines) == 0 || !strings.Contains(lines[len(lines)-1], "ERROR") {
		t.Errorf("last line missing expected prefix: %q", data)
	}
}
