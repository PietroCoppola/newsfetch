package config_test

import (
	"os"
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
	_ = time.Time{} // keep time import
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
