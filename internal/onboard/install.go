package onboard

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ErrAlreadyInstalled is returned by InitFlow when a config file or rc-block
// already exists. The flow refuses rather than repairing partial state — see
// CLAUDE.md / spec.md discussion: repair logic is a bug magnet, and the user
// can always rerun --init after deleting the offending file.
var ErrAlreadyInstalled = errors.New("newsfetch is already installed")

// InitDeps wires InitFlow to its dependencies. Production constructs one with
// the real implementations; tests inject stubs so they can run in a tempdir
// without touching the user's $HOME or hitting Hacker News.
type InitDeps struct {
	ConfigPath func() (string, error)  // resolves the target config.toml path
	Shell      func() (Shell, error)   // detects the user's shell + rc path
	Answers    func() (Answers, error) // collects wizard answers (TUI in prod, canned in tests)
	WarmCache  func() error            // runs newsfetch once to populate the cache
	Out        io.Writer               // human-readable status messages
}

// InitFlow drives the --init pipeline: detect → check pre-existing state →
// wizard → write config → patch rc → warm cache. Each step is fail-fast; a
// refusal at the pre-existing-state check happens before the wizard runs so
// the user is not asked questions that won't be saved.
func InitFlow(d InitDeps) error {
	configPath, err := d.ConfigPath()
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	sh, err := d.Shell()
	if err != nil {
		return err
	}

	if existing := preExisting(configPath, sh.RCPath); existing != "" {
		return fmt.Errorf("%w: %s", ErrAlreadyInstalled, existing)
	}

	answers, err := d.Answers()
	if err != nil {
		return fmt.Errorf("wizard: %w", err)
	}

	if err := WriteConfig(configPath, answers.Topics, answers.Style); err != nil {
		return err
	}
	if _, err := PatchRC(sh.RCPath); err != nil {
		return fmt.Errorf("patch rc: %w", err)
	}

	fmt.Fprintf(d.Out, "newsfetch: wrote %s\n", configPath)
	fmt.Fprintf(d.Out, "newsfetch: patched %s (open a new terminal to see it)\n", sh.RCPath)

	if err := d.WarmCache(); err != nil {
		// Don't fail the whole flow — the user is configured; the cache
		// can populate on next shell start.
		fmt.Fprintf(d.Out, "newsfetch: warm-cache step failed (%v); next terminal open will retry\n", err)
	}
	return nil
}

// preExisting returns a human-readable description of the first pre-existing
// piece of newsfetch state it finds, or "" if neither config nor rc-block is
// present. Used to construct the ErrAlreadyInstalled message.
func preExisting(configPath, rcPath string) string {
	if _, err := os.Stat(configPath); err == nil {
		return configPath
	}
	if data, err := os.ReadFile(rcPath); err == nil {
		if _, _, ok := findBlock(string(data)); ok {
			return rcPath
		}
	}
	return ""
}

// PatchRC reads rcPath, inserts (or refreshes) the newsfetch block, and writes
// the file back atomically (temp file + rename in the same directory). Parent
// directories are created if missing — fish in particular often needs
// ~/.config/fish/ created on first install.
func PatchRC(rcPath string) (changed bool, err error) {
	if err := os.MkdirAll(filepath.Dir(rcPath), 0o755); err != nil {
		return false, fmt.Errorf("create rc dir: %w", err)
	}
	data, err := os.ReadFile(rcPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read rc: %w", err)
	}
	updated, changed := Insert(string(data))
	if !changed {
		return false, nil
	}
	if err := atomicWrite(rcPath, []byte(updated), 0o644); err != nil {
		return false, fmt.Errorf("write rc: %w", err)
	}
	return true, nil
}

// atomicWrite writes data to a temp file in the same directory as path, then
// renames over path. Same-directory rename is atomic on POSIX, so a crash
// mid-write leaves either the old file or the new file — never a truncated
// half-write of someone's .bashrc.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".newsfetch-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op once the rename succeeds
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
