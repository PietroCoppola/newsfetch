package onboard

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// UninstallDeps wires UninstallFlow to its dependencies. Same pattern as
// InitDeps: production fills in real functions, tests inject stubs.
type UninstallDeps struct {
	ConfigPath func() (string, error)
	CachePath  func() (string, error)
	Shell      func() (Shell, error)
	Out        io.Writer
}

// UninstallFlow removes the newsfetch block from the user's rc file, if
// present. Config and cache files are left in place intentionally — the user
// may want to keep their topic selection — and their paths are printed so
// the user can decide whether to `rm` them. Safe to re-run: missing rc file,
// absent block, or already-clean state all succeed with an explanatory line.
func UninstallFlow(d UninstallDeps) error {
	sh, err := d.Shell()
	if err != nil {
		return err
	}
	configPath, err := d.ConfigPath()
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	cachePath, err := d.CachePath()
	if err != nil {
		return fmt.Errorf("resolve cache path: %w", err)
	}

	data, err := os.ReadFile(sh.RCPath)
	switch {
	case err == nil:
		updated, changed := Remove(string(data))
		if !changed {
			fmt.Fprintf(d.Out, "newsfetch: no block found in %s (nothing to remove)\n", sh.RCPath)
		} else {
			if err := atomicWrite(sh.RCPath, []byte(updated), 0o644); err != nil {
				return fmt.Errorf("write rc: %w", err)
			}
			fmt.Fprintf(d.Out, "newsfetch: removed block from %s\n", sh.RCPath)
		}
	case errors.Is(err, os.ErrNotExist):
		fmt.Fprintf(d.Out, "newsfetch: no rc file at %s (nothing to remove)\n", sh.RCPath)
	default:
		return fmt.Errorf("read rc: %w", err)
	}

	// Report what we deliberately didn't touch, so the user can clean up if
	// they actually want a full wipe.
	if _, err := os.Stat(configPath); err == nil {
		fmt.Fprintf(d.Out, "newsfetch: config left in place at %s (rm to remove)\n", configPath)
	}
	if _, err := os.Stat(cachePath); err == nil {
		fmt.Fprintf(d.Out, "newsfetch: cache left in place at %s (rm to remove)\n", cachePath)
	}
	return nil
}
