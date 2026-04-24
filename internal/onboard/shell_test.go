package onboard

import (
	"strings"
	"testing"
)

func TestDetectShell(t *testing.T) {
	const home = "/Users/test"
	cases := []struct {
		name     string
		shellEnv string
		goos     string
		wantName string
		wantRC   string
		wantErr  bool
	}{
		{"zsh", "/bin/zsh", "darwin", "zsh", home + "/.zshrc", false},
		{"zsh on linux", "/usr/bin/zsh", "linux", "zsh", home + "/.zshrc", false},
		{"bash on darwin uses bash_profile", "/opt/homebrew/bin/bash", "darwin", "bash", home + "/.bash_profile", false},
		{"bash on linux uses bashrc", "/usr/bin/bash", "linux", "bash", home + "/.bashrc", false},
		{"fish", "/usr/local/bin/fish", "darwin", "fish", home + "/.config/fish/config.fish", false},
		{"unknown shell", "/bin/tcsh", "linux", "", "", true},
		{"empty SHELL", "", "linux", "", "", true},
		{"trailing args tolerated", "/bin/zsh -l", "linux", "", "", true}, // strictness: bare path only
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := detectShell(tc.shellEnv, home, tc.goos)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if got.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tc.wantName)
			}
			if got.RCPath != tc.wantRC {
				t.Errorf("RCPath = %q, want %q", got.RCPath, tc.wantRC)
			}
		})
	}
}

func TestDetectShellUnknownErrorMentionsShell(t *testing.T) {
	_, err := detectShell("/bin/tcsh", "/home/u", "linux")
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "tcsh") {
		t.Errorf("error should name the unsupported shell; got %q", err)
	}
}
