package render

import (
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
)

// Statusline renders s as a single no-chrome line for embedding in a status
// row (the Claude Code statusline): the title alone — no host, age, or
// author, which eat width — truncated to maxWidth display columns and
// wrapped in an OSC 8 hyperlink to the story URL, underlined so
// clickability is discoverable. maxWidth <= 0 disables truncation.
//
// The underline closes with SGR 24 (underline off), never SGR 0 (full
// reset): status rows commonly wrap segments in dim, and a full reset
// would clear the surrounding dim so the rest of the row jumps to full
// brightness.
//
// Truncation counts display columns (runewidth), not bytes or runes:
// downstream cut/awk truncation is byte-based and breaks on emoji/CJK.
// The escape sequences are zero display columns and sit outside the
// truncation budget. A story without a URL renders as plain text — no
// link, and no underline, because the underline's only job is signalling
// clickability.
func Statusline(s fetch.Story, maxWidth int) string {
	title := s.Title
	if maxWidth > 0 {
		title = runewidth.Truncate(title, maxWidth, ellipsis)
	}
	if s.URL == "" {
		return title + "\n"
	}
	return osc8Open + s.URL + osc8Term + underlineOn + title + underlineOff + osc8Open + osc8Term + "\n"
}
