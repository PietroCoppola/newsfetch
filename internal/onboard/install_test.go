package onboard

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixedDeps builds an InitDeps that points at tempdir paths and returns
// canned wizard answers — the recipe shared by every InitFlow test.
func fixedDeps(t *testing.T, configPath, rcPath string, answers Answers) InitDeps {
	t.Helper()
	warmCalled := 0
	deps := InitDeps{
		ConfigPath: func() (string, error) { return configPath, nil },
		Shell:      func() (Shell, error) { return Shell{Name: "bash", RCPath: rcPath}, nil },
		Answers:    func() (Answers, error) { return answers, nil },
		WarmCache:  func() error { warmCalled++; return nil },
		Out:        &bytes.Buffer{},
	}
	t.Cleanup(func() {
		if warmCalled > 1 {
			t.Errorf("WarmCache called %d times; want at most 1", warmCalled)
		}
	})
	return deps
}

func TestInitFlow_WritesConfigAndPatchesRC(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	rcPath := filepath.Join(dir, ".bashrc")
	if err := os.WriteFile(rcPath, []byte("# existing\nalias ll='ls -l'\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	deps := fixedDeps(t, configPath, rcPath, Answers{Topics: []string{"rust"}, Style: "boxed"})
	if err := InitFlow(deps); err != nil {
		t.Fatalf("InitFlow: %v", err)
	}

	cfg, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if !strings.Contains(string(cfg), "rust") || !strings.Contains(string(cfg), "boxed") {
		t.Errorf("config missing fields:\n%s", cfg)
	}

	rc, err := os.ReadFile(rcPath)
	if err != nil {
		t.Fatalf("rc not readable: %v", err)
	}
	rcStr := string(rc)
	if !strings.Contains(rcStr, BeginMarker) || !strings.Contains(rcStr, EndMarker) {
		t.Errorf("rc missing block markers:\n%s", rcStr)
	}
	if !strings.Contains(rcStr, "alias ll='ls -l'") {
		t.Errorf("rc lost original content:\n%s", rcStr)
	}
}

func TestInitFlow_CreatesRCWhenMissing(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	rcPath := filepath.Join(dir, "subdir", ".bashrc") // parent dir missing
	deps := fixedDeps(t, configPath, rcPath, Answers{Topics: nil, Style: "minimal"})

	if err := InitFlow(deps); err != nil {
		t.Fatalf("InitFlow: %v", err)
	}
	if _, err := os.Stat(rcPath); err != nil {
		t.Fatalf("rc not created: %v", err)
	}
}

func TestInitFlow_RefusesWhenConfigExists(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	rcPath := filepath.Join(dir, ".bashrc")
	if err := os.WriteFile(configPath, []byte("topics = [\"go\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	deps := fixedDeps(t, configPath, rcPath, Answers{Topics: []string{"rust"}, Style: "boxed"})
	err := InitFlow(deps)
	if !errors.Is(err, ErrAlreadyInstalled) {
		t.Fatalf("err = %v, want ErrAlreadyInstalled", err)
	}
	if !strings.Contains(err.Error(), configPath) {
		t.Errorf("error should name the config path; got: %v", err)
	}

	// Existing config must be untouched.
	got, _ := os.ReadFile(configPath)
	if !strings.Contains(string(got), `"go"`) {
		t.Errorf("existing config clobbered:\n%s", got)
	}
}

func TestInitFlow_RefusesWhenRCBlockExists(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	rcPath := filepath.Join(dir, ".bashrc")
	rcContent := "# rc\n" + BeginMarker + "\nnewsfetch\n" + EndMarker + "\n"
	if err := os.WriteFile(rcPath, []byte(rcContent), 0o644); err != nil {
		t.Fatal(err)
	}

	deps := fixedDeps(t, configPath, rcPath, Answers{Topics: nil, Style: "boxed"})
	err := InitFlow(deps)
	if !errors.Is(err, ErrAlreadyInstalled) {
		t.Fatalf("err = %v, want ErrAlreadyInstalled", err)
	}
	if !strings.Contains(err.Error(), rcPath) {
		t.Errorf("error should name the rc path; got: %v", err)
	}

	// Config must NOT have been written (refusal happens pre-write).
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("config was written despite refusal: %v", err)
	}
}

func TestInitFlow_WizardErrorAborts(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	rcPath := filepath.Join(dir, ".bashrc")

	deps := InitDeps{
		ConfigPath: func() (string, error) { return configPath, nil },
		Shell:      func() (Shell, error) { return Shell{Name: "bash", RCPath: rcPath}, nil },
		Answers:    func() (Answers, error) { return Answers{}, errors.New("user cancelled") },
		WarmCache:  func() error { t.Fatal("WarmCache should not be called when wizard errors"); return nil },
		Out:        &bytes.Buffer{},
	}

	err := InitFlow(deps)
	if err == nil {
		t.Fatal("want error from cancelled wizard")
	}
	if _, statErr := os.Stat(configPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("config written despite wizard cancel: %v", statErr)
	}
}

func TestInitFlow_OutputMentionsPaths(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	rcPath := filepath.Join(dir, ".bashrc")
	out := &bytes.Buffer{}
	deps := InitDeps{
		ConfigPath: func() (string, error) { return configPath, nil },
		Shell:      func() (Shell, error) { return Shell{Name: "bash", RCPath: rcPath}, nil },
		Answers:    func() (Answers, error) { return Answers{Style: "boxed"}, nil },
		WarmCache:  func() error { return nil },
		Out:        out,
	}
	if err := InitFlow(deps); err != nil {
		t.Fatalf("InitFlow: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, configPath) {
		t.Errorf("output should mention config path; got:\n%s", got)
	}
	if !strings.Contains(got, rcPath) {
		t.Errorf("output should mention rc path; got:\n%s", got)
	}
}
