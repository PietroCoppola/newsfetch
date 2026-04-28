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
	b.WriteString(boxTopLeft + horiz + boxTopRight + "\n")
	b.WriteString(boxVert + " " + padRight(title, contentWidth) + " " + boxVert + "\n")
	b.WriteString(boxVert + " " + padRight(meta, contentWidth) + " " + boxVert + "\n")
	b.WriteString(boxBotLeft + horiz + boxBotRight + "\n")
	return b.String()
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
