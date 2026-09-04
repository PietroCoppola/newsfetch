// Package render produces the terminal-ready output for a Story.
package render

import (
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PietroCoppola/newsfetch/internal/fetch"
)

const (
	boxHoriz    = "─"
	boxVert     = "│"
	boxTopLeft  = "╭"
	boxTopRight = "╮"
	boxBotLeft  = "╰"
	boxBotRight = "╯"
	boxLeftTee  = "├"
	boxRightTee = "┤"
	boxDownTee  = "┬"
	ellipsis    = "…"
	// minWidth is the floor for content legibility - narrower panels swap
	// to the caller's problem; M1 never produces them.
	minWidth = 10
)

// Boxed renders s as a bordered Unicode panel width columns wide. The now
// argument is the reference point for the "X ago" relative timestamp so that
// output is deterministic for tests.
func Boxed(s fetch.Story, now time.Time, width int) string {
	return boxedLabelled(s, now, width, "")
}

// boxedLabelled is [Boxed] with an optional pool label written into the top
// border. It exists so the multi-pool stacker can label a box without
// keeping a second copy of the panel body; [Boxed] is the empty-label case
// and stays byte-for-byte what it always was.
func boxedLabelled(s fetch.Story, now time.Time, width int, label string) string {
	if width < minWidth {
		width = minWidth
	}
	contentWidth := width - 4 // two corners plus one space of padding each side
	if contentWidth < 1 {
		contentWidth = 1
	}

	title := truncate(s.Title, contentWidth)
	meta := truncate(metaLine(s, now), contentWidth)

	horiz := strings.Repeat(boxHoriz, width-2)

	var b strings.Builder
	b.WriteString(topBorder(width, label) + "\n")
	b.WriteString(boxVert + " " + padRight(title, contentWidth) + " " + boxVert + "\n")
	b.WriteString(boxVert + " " + padRight(meta, contentWidth) + " " + boxVert + "\n")
	b.WriteString(boxBotLeft + horiz + boxBotRight + "\n")
	return b.String()
}

// topBorder builds a box's top edge, width columns wide, with no trailing
// newline. It is the single home for that edge so every box in the package
// keeps the same shape.
//
// A non-empty label is written into the border as "╭─ Label ───". The label
// never widens the box: stacked pool boxes must align, so a label that will
// not fit is truncated, and one that cannot be truncated to at least one
// rune is dropped in favour of the plain border. That is also what keeps
// strings.Repeat off a negative count at pathological widths.
func topBorder(width int, label string) string {
	const (
		// corners, the one leading dash, and the two spaces around the label
		labelChrome = 5
		// corners only
		plainChrome = 2
	)
	if label != "" {
		fill := width - labelChrome - utf8.RuneCountInString(label)
		if fill < 1 {
			// Leave room for exactly one trailing dash, then re-measure:
			// truncate can only shrink the label, so fill lands at >= 1
			// unless the width cannot hold a label at all.
			label = truncate(label, width-labelChrome-1)
			fill = width - labelChrome - utf8.RuneCountInString(label)
		}
		if fill >= 1 {
			return boxTopLeft + boxHoriz + " " + label + " " + strings.Repeat(boxHoriz, fill) + boxTopRight
		}
	}
	n := width - plainChrome
	if n < 0 {
		n = 0
	}
	return boxTopLeft + strings.Repeat(boxHoriz, n) + boxTopRight
}

// Fallback renders the neutral "no fresh news" message the caller passes in.
// It is used when the cache is missing and the fetcher fails - for example,
// offline on first run.
func Fallback(message string) string {
	return message + "\n"
}

func metaLine(s fetch.Story, now time.Time) string {
	parts := []string{hostname(s.URL), relativeAge(s.CreatedAt, now)}
	if s.Author != "" {
		parts = append(parts, "by "+s.Author)
	}
	return strings.Join(parts, " · ")
}

func hostname(rawURL string) string {
	if rawURL == "" {
		return "unknown"
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "unknown"
	}
	return strings.TrimPrefix(u.Host, "www.")
}

func relativeAge(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	}
}

func padRight(s string, width int) string {
	pad := width - utf8.RuneCountInString(s)
	if pad <= 0 {
		return s
	}
	return s + strings.Repeat(" ", pad)
}

func truncate(s string, width int) string {
	if utf8.RuneCountInString(s) <= width {
		return s
	}
	if width <= 1 {
		return ellipsis
	}
	runes := []rune(s)
	return string(runes[:width-1]) + ellipsis
}
