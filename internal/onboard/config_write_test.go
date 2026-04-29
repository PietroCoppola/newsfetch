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
	if err := WriteConfig(path, Answers{Topics: []string{"rust", "go"}, Style: "boxed"}); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config not written: %v", err)
	}
}

func TestWriteConfig_RoundTripsThroughConfigLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	topics := []string{"rust", "databases"}
	if err := WriteConfig(path, Answers{Topics: topics, Style: "minimal"}); err != nil {
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
	if err := WriteConfig(path, Answers{Topics: nil, Style: "boxed"}); err != nil {
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

func TestWriteConfig_NilSourcesOmitsLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteConfig(path, Answers{Topics: nil, Style: "boxed", Sources: nil}); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "sources") {
		t.Errorf("file should not mention sources when Answers.Sources is nil; got:\n%s", data)
	}
}

func TestWriteConfig_NonNilSourcesEmitsLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteConfig(path, Answers{Topics: nil, Style: "boxed", Sources: []string{"hackernews", "lobsters"}}); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if !reflect.DeepEqual(cfg.Sources, []string{"hackernews", "lobsters"}) {
		t.Errorf("Sources = %v, want [hackernews lobsters]", cfg.Sources)
	}
}

func TestWriteConfig_RefusesToOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteConfig(path, Answers{Topics: []string{"rust"}, Style: "boxed"}); err != nil {
		t.Fatalf("first WriteConfig: %v", err)
	}
	err := WriteConfig(path, Answers{Topics: []string{"go"}, Style: "minimal"})
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

func TestOverwriteConfig_ReplacesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteConfig(path, Answers{Topics: []string{"rust"}, Style: "boxed"}); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	if err := OverwriteConfig(path, Answers{Topics: []string{"go"}, Style: "minimal", Sources: []string{"hackernews"}}); err != nil {
		t.Fatalf("OverwriteConfig: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if !reflect.DeepEqual(cfg.Topics, []string{"go"}) {
		t.Errorf("Topics = %v, want [go]", cfg.Topics)
	}
	if cfg.Style != "minimal" {
		t.Errorf("Style = %q, want minimal", cfg.Style)
	}
	if !reflect.DeepEqual(cfg.Sources, []string{"hackernews"}) {
		t.Errorf("Sources = %v, want [hackernews]", cfg.Sources)
	}
}

func TestWriteConfig_CountAndTickerRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	answers := Answers{
		Topics:       nil,
		Style:        "boxed",
		Sources:      []string{"hackernews"},
		Count:        3,
		TickerMarker: "branch",
		TickerBoxed:  true,
	}
	if err := WriteConfig(path, answers); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if cfg.Count != 3 {
		t.Errorf("Count = %d, want 3", cfg.Count)
	}
	if cfg.TickerMarker != "branch" {
		t.Errorf("TickerMarker = %q, want branch", cfg.TickerMarker)
	}
	if !cfg.TickerBoxed {
		t.Errorf("TickerBoxed = %v, want true", cfg.TickerBoxed)
	}
}

func TestWriteConfig_TickerFieldsEmittedEvenWhenInert(t *testing.T) {
	// User has style=minimal (ticker fields are inert) but their TickerMarker
	// is set to "branch" from a prior config. The writer must persist it so a
	// future switch back to style=boxed restores the prior tuning instead of
	// silently reverting to the default.
	path := filepath.Join(t.TempDir(), "config.toml")
	answers := Answers{
		Topics:       nil,
		Style:        "minimal",
		Sources:      []string{"hackernews"},
		Count:        1,
		TickerMarker: "branch",
		TickerBoxed:  true,
	}
	if err := WriteConfig(path, answers); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	data, _ := os.ReadFile(path)
	body := string(data)
	if !strings.Contains(body, "ticker_marker") || !strings.Contains(body, "branch") {
		t.Errorf("ticker_marker not persisted; got:\n%s", body)
	}
	if !strings.Contains(body, "ticker_boxed = true") {
		t.Errorf("ticker_boxed not persisted; got:\n%s", body)
	}
}

func TestOverwriteConfig_CreatesWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "newsubdir", "config.toml")
	if err := OverwriteConfig(path, Answers{Topics: nil, Style: "boxed", Sources: []string{"hackernews"}}); err != nil {
		t.Fatalf("OverwriteConfig: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config not written: %v", err)
	}
}
