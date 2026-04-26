package onboard

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// ErrNoConfig is returned by SettingsFlow when the target config file does
// not exist. --settings is the explicit edit-existing path; --init is the
// bootstrap path. Conflating them would blur the M3 / M4.5 contract.
var ErrNoConfig = errors.New("no config to edit")

// SettingsDeps wires SettingsFlow to its dependencies. Same shape as
// InitDeps: production fills in the real implementations; tests inject
// stubs to run against a tempdir without touching $HOME.
type SettingsDeps struct {
	ConfigPath func() (string, error)                 // resolves the target config.toml path
	Current    func(path string) (Answers, error)     // loads the existing config as Answers
	Answers    func(current Answers) (Answers, error) // wizard or JSON; receives current for prefill
	Out        io.Writer
}

// SettingsFlow drives the --settings pipeline: resolve config path → refuse
// if missing → load current values → run wizard / read JSON → overwrite
// config. Does not touch the shell rc, the cache, or the warm-cache path —
// per spec, --settings is purely an edit-existing-config operation.
func SettingsFlow(d SettingsDeps) error {
	configPath, err := d.ConfigPath()
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w at %s — run `newsfetch --init` first", ErrNoConfig, configPath)
	} else if err != nil {
		return fmt.Errorf("stat config: %w", err)
	}

	current, err := d.Current(configPath)
	if err != nil {
		return fmt.Errorf("load current config: %w", err)
	}
	answers, err := d.Answers(current)
	if err != nil {
		return fmt.Errorf("wizard: %w", err)
	}
	if err := OverwriteConfig(configPath, answers); err != nil {
		return err
	}
	fmt.Fprintf(d.Out, "newsfetch: updated %s\n", configPath)
	return nil
}
