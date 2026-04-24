// Package onboard implements the --init and --uninstall flows: interactive
// setup, config file writing, and shell rc patching. It is scoped to those
// subcommands and must not be imported from the default render path — it
// pulls in an interactive-TUI dependency (huh) that would inflate startup.
package onboard

import "strings"

// BeginMarker and EndMarker delimit the newsfetch-managed block in a user's
// shell rc file. They are stable across versions so --uninstall can find and
// remove blocks installed by older binaries.
const (
	BeginMarker = "# >>> newsfetch >>>"
	EndMarker   = "# <<< newsfetch <<<"
)

// blockBody is the command(s) we run inside the delimited block. Kept simple
// on purpose — invoking the binary by bare name lets the user's $PATH resolve
// whichever newsfetch build is installed.
const blockBody = "newsfetch"

// block returns the canonical block text (markers + body), newline-terminated.
func block() string {
	return BeginMarker + "\n" + blockBody + "\n" + EndMarker + "\n"
}

// Insert adds the newsfetch block to content. If a block already exists
// (matched by markers) it is replaced so stale bodies from older installs
// get refreshed. The second return value reports whether content changed.
func Insert(content string) (string, bool) {
	desired := block()
	if start, end, ok := findBlock(content); ok {
		// Replace existing block in-place.
		updated := content[:start] + desired + content[end:]
		if updated == content {
			return content, false
		}
		return updated, true
	}
	// Append. Ensure exactly one blank-line separator from any existing content.
	if content == "" {
		return desired, true
	}
	trimmed := strings.TrimRight(content, "\n")
	return trimmed + "\n\n" + desired, true
}

// Remove strips the newsfetch block from content if present. It also collapses
// the blank-line separator that Insert added, so round-tripping Insert then
// Remove restores the original content byte-for-byte. If no block is present,
// content is returned unchanged with changed=false.
func Remove(content string) (string, bool) {
	start, end, ok := findBlock(content)
	if !ok {
		return content, false
	}
	// If the block was appended with a "\n\n" separator, eat one of those
	// newlines so we don't leave a double blank line behind.
	before := content[:start]
	after := content[end:]
	if strings.HasSuffix(before, "\n\n") {
		before = before[:len(before)-1]
	}
	return before + after, true
}

// findBlock locates the first complete block in content. It returns the byte
// offsets [start, end) covering the begin marker through the trailing newline
// after the end marker (or the end marker itself if no trailing newline).
func findBlock(content string) (start, end int, ok bool) {
	b := strings.Index(content, BeginMarker)
	if b < 0 {
		return 0, 0, false
	}
	e := strings.Index(content[b:], EndMarker)
	if e < 0 {
		return 0, 0, false
	}
	endOfEndMarker := b + e + len(EndMarker)
	// Include the trailing newline, if any.
	if endOfEndMarker < len(content) && content[endOfEndMarker] == '\n' {
		endOfEndMarker++
	}
	return b, endOfEndMarker, true
}
