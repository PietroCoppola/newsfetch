package onboard

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fixedSettingsDeps wires a SettingsDeps that points at tempdir paths and
// returns canned values. Mirrors the helper for InitFlow tests.
func fixedSettingsDeps(t *testing.T, configPath string, current, answers Answers) SettingsDeps {
	t.Helper()
	return SettingsDeps{
		ConfigPath: func() (string, error) { return configPath, nil },
		Current:    func(string) (Answers, error) { return current, nil },
		Answers:    func(Answers) (Answers, error) { return answers, nil },
		Out:        &bytes.Buffer{},
	}
}

func TestSettingsFlow_OverwritesConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	// Pre-create the config so SettingsFlow doesn't refuse.
	if err := os.WriteFile(configPath, []byte("topics = [\"old\"]\nstyle = \"boxed\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	current := Answers{Topics: []string{"old"}, Style: "boxed", Sources: []string{"hackernews"}}
	answers := Answers{Topics: []string{"rust"}, Style: "minimal", Sources: []string{"hackernews", "lobsters"}}

	if err := SettingsFlow(fixedSettingsDeps(t, configPath, current, answers)); err != nil {
		t.Fatalf("SettingsFlow: %v", err)
	}

	got, _ := os.ReadFile(configPath)
	gotStr := string(got)
	if !strings.Contains(gotStr, `topics = ["rust"]`) {
		t.Errorf("topics not updated:\n%s", gotStr)
	}
	if !strings.Contains(gotStr, `style = "minimal"`) {
		t.Errorf("style not updated:\n%s", gotStr)
	}
	if !strings.Contains(gotStr, `sources = ["hackernews", "lobsters"]`) {
		t.Errorf("sources not updated:\n%s", gotStr)
	}
	if strings.Contains(gotStr, `"old"`) {
		t.Errorf("old content survived:\n%s", gotStr)
	}
}

func TestSettingsFlow_RefusesWhenConfigMissing(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	// File deliberately not created.

	deps := SettingsDeps{
		ConfigPath: func() (string, error) { return configPath, nil },
		Current: func(string) (Answers, error) {
			t.Fatal("Current should not be called when config is missing")
			return Answers{}, nil
		},
		Answers: func(Answers) (Answers, error) {
			t.Fatal("Answers should not be called when config is missing")
			return Answers{}, nil
		},
		Out: &bytes.Buffer{},
	}
	err := SettingsFlow(deps)
	if !errors.Is(err, ErrNoConfig) {
		t.Fatalf("err = %v, want ErrNoConfig", err)
	}
	if !strings.Contains(err.Error(), "--init") {
		t.Errorf("error should suggest running --init; got: %v", err)
	}
}

func TestSettingsFlow_AnswersErrorAborts(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	original := []byte("topics = [\"keep\"]\nstyle = \"boxed\"\n")
	if err := os.WriteFile(configPath, original, 0o644); err != nil {
		t.Fatal(err)
	}

	deps := SettingsDeps{
		ConfigPath: func() (string, error) { return configPath, nil },
		Current:    func(string) (Answers, error) { return Answers{}, nil },
		Answers:    func(Answers) (Answers, error) { return Answers{}, errors.New("user cancelled") },
		Out:        &bytes.Buffer{},
	}
	if err := SettingsFlow(deps); err == nil {
		t.Fatal("expected error from cancelled wizard")
	}
	got, _ := os.ReadFile(configPath)
	if !reflect.DeepEqual(got, original) {
		t.Errorf("config modified despite wizard cancel:\nwant %q\ngot  %q", original, got)
	}
}

func TestSettingsFlow_PassesCurrentToAnswersFn(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte("topics = []\nstyle = \"boxed\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	current := Answers{Topics: []string{"go"}, Style: "json", Sources: []string{"lobsters"}}
	var seen Answers
	deps := SettingsDeps{
		ConfigPath: func() (string, error) { return configPath, nil },
		Current:    func(string) (Answers, error) { return current, nil },
		Answers: func(c Answers) (Answers, error) {
			seen = c
			return Answers{Topics: nil, Style: "boxed", Sources: []string{"hackernews"}}, nil
		},
		Out: &bytes.Buffer{},
	}
	if err := SettingsFlow(deps); err != nil {
		t.Fatalf("SettingsFlow: %v", err)
	}
	if !reflect.DeepEqual(seen, current) {
		t.Errorf("AnswersFn received %+v, want current %+v", seen, current)
	}
}

func TestSettingsFlow_OutputMentionsPath(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte("topics = []\nstyle = \"boxed\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	deps := SettingsDeps{
		ConfigPath: func() (string, error) { return configPath, nil },
		Current:    func(string) (Answers, error) { return Answers{}, nil },
		Answers:    func(Answers) (Answers, error) { return Answers{Style: "boxed", Sources: []string{"hackernews"}}, nil },
		Out:        out,
	}
	if err := SettingsFlow(deps); err != nil {
		t.Fatalf("SettingsFlow: %v", err)
	}
	if !strings.Contains(out.String(), configPath) {
		t.Errorf("output should mention config path; got:\n%s", out.String())
	}
}
