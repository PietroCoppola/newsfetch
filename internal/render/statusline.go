package render

import (
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/PietroCoppola/newsfetch/internal/fetch"
)

// OSC 8 hyperlink framing. ST (ESC \) terminates both the open and close
// sequences; verified end-to-end in Ghostty through Claude Code's Ink
// rendering layer (see docs/planning/statusline.md), also supported by
// iTerm2, Kitty, and WezTerm. Terminals without OSC 8 support display the
// title as plain text.
const (
	osc8Open     = "\x1b]8;;"
	osc8Term     = "\x1b\\"
	underlineOn  = "\x1b[4m"
	underlineOff = "\x1b[24m"
	dimOn        = "\x1b[2m"
	dimOff       = "\x1b[22m"
)

// Statusline renders s as a single no-chrome line for embedding in a status
// row (the Claude Code statusline): the title, then a dim metadata tail of
// host, age, and author. now is the reference point for the relative age so
// output is deterministic for tests. The line is truncated to maxWidth
// display columns; maxWidth <= 0 disables truncation.
//
// The title is wrapped in an OSC 8 hyperlink to the story URL and
// underlined so clickability is discoverable. The tail is neither linked
// nor underlined: it is not clickable, and underlining it would advertise
// otherwise. A story without a URL renders its title as plain text — no
// link, and no underline, because the underline's only job is signalling
// clickability — while the tail still reports the metadata, with Minimal's
// "unknown" host.
//
// The underline closes with SGR 24 (underline off) and the dim with SGR 22
// (dim off), never SGR 0 (full reset): status rows commonly wrap segments
// in dim, and a full reset would clear the surrounding dim so the rest of
// the row jumps to full brightness.
//
// Truncation counts display columns (runewidth), not bytes or runes:
// downstream cut/awk truncation is byte-based and breaks on emoji/CJK. The
// escape sequences are zero display columns and sit outside the truncation
// budget. The tail is charged against the budget first and the title takes
// what is left, so metadata survives a squeeze while the headline shrinks —
// the same principle as a ticker line. When the tail alone would fill the
// budget the tail is dropped instead: a title stub beats metadata with no
// headline attached.
func Statusline(s fetch.Story, now time.Time, maxWidth int) string {
	tail := statuslineTail(s, now)
	title := s.Title
	if maxWidth > 0 {
		if tailWidth := runewidth.StringWidth(tail); tailWidth >= maxWidth-1 {
			tail = ""
			title = runewidth.Truncate(title, maxWidth, ellipsis)
		} else {
			title = runewidth.Truncate(title, maxWidth-tailWidth, ellipsis)
		}
	}
	if tail != "" {
		tail = dimOn + tail + dimOff
	}
	if s.URL == "" {
		return title + tail + "\n"
	}
	return osc8Open + s.URL + osc8Term + underlineOn + title + underlineOff + osc8Open + osc8Term + tail + "\n"
}

// statuslineTail builds the plain (unescaped) metadata tail, mirroring
// Minimal's grammar so the two styles read the same: host, age, and the
// author when the story has one.
func statuslineTail(s fetch.Story, now time.Time) string {
	parts := []string{hostname(s.URL), relativeAge(s.CreatedAt, now)}
	if s.Author != "" {
		parts = append(parts, "by "+s.Author)
	}
	return " · " + strings.Join(parts, " · ")
}
