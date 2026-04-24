package onboard

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newUninstallDeps(configPath, cachePath, rcPath string, out *bytes.Buffer) UninstallDeps {
	return UninstallDeps{
		ConfigPath: func() (string, error) { return configPath, nil },
		CachePath:  func() (string, error) { return cachePath, nil },
		Shell:      func() (Shell, error) { return Shell{Name: "bash", RCPath: rcPath}, nil },
		Out:        out,
	}
}

func TestUninstallFlow_StripsBlock(t *testing.T) {
	dir := t.TempDir()
	rcPath := filepath.Join(dir, ".bashrc")
	rcOrig := "# rc\nalias ll='ls -l'\n"
	rcWithBlock := rcOrig + "\n" + BeginMarker + "\nnewsfetch\n" + EndMarker + "\n"
	if err := os.WriteFile(rcPath, []byte(rcWithBlock), 0o644); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	deps := newUninstallDeps(
		filepath.Join(dir, "config.toml"),
		filepath.Join(dir, "cache.json"),
		rcPath, out,
	)
	if err := UninstallFlow(deps); err != nil {
		t.Fatalf("UninstallFlow: %v", err)
	}
	got, _ := os.ReadFile(rcPath)
	if strings.Contains(string(got), BeginMarker) {
		t.Errorf("block still present:\n%s", got)
	}
	if !strings.Contains(string(got), "alias ll='ls -l'") {
		t.Errorf("rc content lost:\n%s", got)
	}
}

func TestUninstallFlow_NoBlockIsNoOp(t *testing.T) {
	dir := t.TempDir()
	rcPath := filepath.Join(dir, ".bashrc")
	original := "# rc\nalias ll='ls -l'\n"
	if err := os.WriteFile(rcPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	deps := newUninstallDeps(
		filepath.Join(dir, "config.toml"),
		filepath.Join(dir, "cache.json"),
		rcPath, out,
	)
	if err := UninstallFlow(deps); err != nil {
		t.Fatalf("UninstallFlow: %v", err)
	}
	got, _ := os.ReadFile(rcPath)
	if string(got) != original {
		t.Errorf("rc modified despite no block:\ngot:\n%s", got)
	}
	if !strings.Contains(out.String(), "nothing") && !strings.Contains(out.String(), "no block") {
		t.Errorf("output should explain no-op; got:\n%s", out.String())
	}
}

func TestUninstallFlow_MissingRCIsNoOp(t *testing.T) {
	dir := t.TempDir()
	rcPath := filepath.Join(dir, "nonexistent", ".bashrc")
	out := &bytes.Buffer{}
	deps := newUninstallDeps(
		filepath.Join(dir, "config.toml"),
		filepath.Join(dir, "cache.json"),
		rcPath, out,
	)
	if err := UninstallFlow(deps); err != nil {
		t.Fatalf("UninstallFlow should succeed when rc missing: %v", err)
	}
	// Must not have created the rc file.
	if _, err := os.Stat(rcPath); !os.IsNotExist(err) {
		t.Errorf("uninstall created rc file; want it left absent")
	}
}

func TestUninstallFlow_ReportsRemainingPaths(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	cachePath := filepath.Join(dir, "cache.json")
	rcPath := filepath.Join(dir, ".bashrc")
	if err := os.WriteFile(configPath, []byte("topics = []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rcPath, []byte(BeginMarker+"\nnewsfetch\n"+EndMarker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	deps := newUninstallDeps(configPath, cachePath, rcPath, out)
	if err := UninstallFlow(deps); err != nil {
		t.Fatalf("UninstallFlow: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, configPath) {
		t.Errorf("output should mention config path:\n%s", got)
	}
	if !strings.Contains(got, cachePath) {
		t.Errorf("output should mention cache path:\n%s", got)
	}
	// Uninstall must NOT delete config or cache.
	if _, err := os.Stat(configPath); err != nil {
		t.Errorf("config deleted by uninstall: %v", err)
	}
	if _, err := os.Stat(cachePath); err != nil {
		t.Errorf("cache deleted by uninstall: %v", err)
	}
}
