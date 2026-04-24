package onboard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Shell describes the user's login shell and the rc file --init should patch.
type Shell struct {
	Name   string // "zsh", "bash", or "fish"
	RCPath string // absolute path to the rc file
}

// ErrUnknownShell is returned by Detect when $SHELL is empty or names a shell
// newsfetch does not know how to patch.
var ErrUnknownShell = errors.New("unsupported shell")

// Detect reads $SHELL and $HOME from the environment and reports the user's
// shell and rc path. It returns ErrUnknownShell (wrapped with the offending
// value) if the shell is unsupported.
func Detect() (Shell, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Shell{}, fmt.Errorf("resolve home dir: %w", err)
	}
	return detectShell(os.Getenv("SHELL"), home, runtime.GOOS)
}

// detectShell is the pure core of Detect. Split out so tests can cover all
// shells and platforms without fiddling with the real environment.
func detectShell(shellEnv, home, goos string) (Shell, error) {
	if shellEnv == "" {
		return Shell{}, fmt.Errorf("%w: $SHELL is empty", ErrUnknownShell)
	}
	name := filepath.Base(shellEnv)
	switch name {
	case "zsh":
		return Shell{Name: "zsh", RCPath: filepath.Join(home, ".zshrc")}, nil
	case "bash":
		// macOS Terminal.app opens login shells, which source .bash_profile
		// rather than .bashrc. On Linux, interactive non-login shells source
		// .bashrc. Patch whichever the user's terminal will actually read.
		if goos == "darwin" {
			return Shell{Name: "bash", RCPath: filepath.Join(home, ".bash_profile")}, nil
		}
		return Shell{Name: "bash", RCPath: filepath.Join(home, ".bashrc")}, nil
	case "fish":
		return Shell{Name: "fish", RCPath: filepath.Join(home, ".config", "fish", "config.fish")}, nil
	default:
		return Shell{}, fmt.Errorf("%w: %s", ErrUnknownShell, name)
	}
}
