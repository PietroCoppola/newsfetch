package onboard

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/PietroCoppola/newsfetch/internal/config"
)

func TestWriteConfig_CreatesFileAndParents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "newsfetch", "config.toml")
	if err := WriteConfig(path, []string{"rust", "go"}, "boxed"); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config not written: %v", err)
	}
}

func TestWriteConfig_RoundTripsThroughConfigLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	topics := []string{"rust", "databases"}
	if err := WriteConfig(path, topics, "minimal"); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if cfg.Style != "minimal" {
		t.Errorf("Style = %q, want %q", cfg.Style, "minimal")
	}
	gotTopics := append([]string(nil), cfg.Topics...)
	sort.Strings(gotTopics)
	wantTopics := append([]string(nil), topics...)
	sort.Strings(wantTopics)
	if !reflect.DeepEqual(gotTopics, wantTopics) {
		t.Errorf("Topics = %v, want %v", gotTopics, wantTopics)
	}
}

func TestWriteConfig_NoTopicsEmitsNoFilter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteConfig(path, nil, "boxed"); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if len(cfg.Topics) != 0 {
		t.Errorf("Topics = %v, want none", cfg.Topics)
	}
}

func TestWriteConfig_RefusesToOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteConfig(path, []string{"rust"}, "boxed"); err != nil {
		t.Fatalf("first WriteConfig: %v", err)
	}
	err := WriteConfig(path, []string{"go"}, "minimal")
	if !errors.Is(err, ErrConfigExists) {
		t.Fatalf("err = %v, want ErrConfigExists", err)
	}
	// Verify original content was NOT overwritten.
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "rust") {
		t.Errorf("original content clobbered")
	}
	if strings.Contains(string(data), "minimal") {
		t.Errorf("second WriteConfig changed file content despite error")
	}
}
