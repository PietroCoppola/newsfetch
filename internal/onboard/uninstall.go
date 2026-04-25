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
	// Confirm asks the user a yes/no question. nil (or one that always
	// returns false) preserves the legacy "leave files in place" behaviour
	// — important for non-TTY invocations where prompting would hang.
	Confirm func(prompt string) bool
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

	maybeRemove(d, configPath, "config")
	maybeRemove(d, cachePath, "cache")
	return nil
}

// maybeRemove prompts the user (when a Confirm is provided) and removes path
// if they say yes. Without a Confirm the file is left in place and its path
// printed, matching the original non-interactive behaviour. Removal failures
// degrade to a warning rather than a hard error — uninstall has already done
// its main job (rc patch reverted) by the time we get here.
func maybeRemove(d UninstallDeps, path, label string) {
	if _, err := os.Stat(path); err != nil {
		return
	}
	if d.Confirm == nil || !d.Confirm(fmt.Sprintf("Remove %s at %s?", label, path)) {
		fmt.Fprintf(d.Out, "newsfetch: %s left in place at %s (rm to remove)\n", label, path)
		return
	}
	if err := os.Remove(path); err != nil {
		fmt.Fprintf(d.Out, "newsfetch: warning: could not remove %s at %s: %v\n", label, path, err)
		return
	}
	fmt.Fprintf(d.Out, "newsfetch: removed %s at %s\n", label, path)
}
