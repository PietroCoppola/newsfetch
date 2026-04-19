package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/config"
	"github.com/PietroCoppola/newsfetch/internal/defaults"
)

func TestDefaults(t *testing.T) {
	got := config.Defaults()
	if got.Style != defaults.Style {
		t.Errorf("Style = %q, want %q", got.Style, defaults.Style)
	}
	if got.CacheTTL != defaults.CacheTTL {
		t.Errorf("CacheTTL = %v, want %v", got.CacheTTL, defaults.CacheTTL)
	}
	if got.MinPoints != defaults.MinPoints {
		t.Errorf("MinPoints = %d, want %d", got.MinPoints, defaults.MinPoints)
	}
	if got.Topics != nil {
		t.Errorf("Topics = %v, want nil", got.Topics)
	}
}

func TestPath(t *testing.T) {
	dir := "/tmp/newsfetch-xdg"
	cases := []struct {
		name    string
		xdg     string
		wantSub string
		wantErr bool
	}{
		{"xdg absolute", dir, dir + "/newsfetch/config.toml", false},
		// "empty" and "unset" are the same code path because Path() uses
		// os.Getenv, which returns "" for both absent and empty-valued
		// variables. Both cases are listed to document intent; do not
		// collapse without also verifying Path() still uses os.Getenv.
		{"xdg empty falls back to home", "", ".config/newsfetch/config.toml", false},
		{"xdg unset falls back to home", "__UNSET__", ".config/newsfetch/config.toml", false},
		{"xdg not absolute falls back to home", "relative/path", ".config/newsfetch/config.toml", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.xdg == "__UNSET__" {
				t.Setenv("XDG_CONFIG_HOME", "")
				_ = os.Unsetenv("XDG_CONFIG_HOME")
			} else {
				t.Setenv("XDG_CONFIG_HOME", tc.xdg)
			}
			got, err := config.Path()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Path() = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Path: %v", err)
			}
			if !strings.HasSuffix(got, tc.wantSub) {
				t.Errorf("Path() = %q, want suffix %q", got, tc.wantSub)
			}
		})
	}
}

func TestLoad_Missing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.toml")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(cfg, config.Defaults()) {
		t.Errorf("Load = %+v, want Defaults() = %+v", cfg, config.Defaults())
	}
}

func TestLoad_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `
topics = ["rust", "ai"]
style = "minimal"
cache_ttl_minutes = 15
min_points = 100
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(cfg.Topics, []string{"rust", "ai"}) {
		t.Errorf("Topics = %v, want [rust ai]", cfg.Topics)
	}
	if cfg.Style != "minimal" {
		t.Errorf("Style = %q, want minimal", cfg.Style)
	}
	if cfg.CacheTTL != 15*time.Minute {
		t.Errorf("CacheTTL = %v, want 15m", cfg.CacheTTL)
	}
	if cfg.MinPoints != 100 {
		t.Errorf("MinPoints = %d, want 100", cfg.MinPoints)
	}
}

func TestLoad_Partial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(`style = "json"`+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Style != "json" {
		t.Errorf("Style = %q, want json", cfg.Style)
	}
	// Untouched fields keep defaults. Topics == nil is the sentinel that
	// drives the no-filter code path in rank.Score; pin it explicitly.
	if cfg.Topics != nil {
		t.Errorf("Topics = %v, want nil (not set in file)", cfg.Topics)
	}
	if cfg.CacheTTL != config.Defaults().CacheTTL {
		t.Errorf("CacheTTL = %v, want default %v", cfg.CacheTTL, config.Defaults().CacheTTL)
	}
	if cfg.MinPoints != config.Defaults().MinPoints {
		t.Errorf("MinPoints = %d, want default %d", cfg.MinPoints, config.Defaults().MinPoints)
	}
}

func TestLoad_UnknownKeysIgnored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `
style = "minimal"
dedupe_history = true
sources = ["hackernews"]
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load (unknown keys should be ignored silently): %v", err)
	}
	if cfg.Style != "minimal" {
		t.Errorf("Style = %q, want minimal", cfg.Style)
	}
}

func TestLoad_ParseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("style = 'boxed\nbroken"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := config.Load(path)
	if err == nil {
		t.Fatal("Load: want parse error, got nil")
	}
	if !reflect.DeepEqual(cfg, config.Defaults()) {
		t.Errorf("Load returned non-default cfg on parse error: %+v", cfg)
	}
}
