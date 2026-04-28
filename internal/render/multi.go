package render

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PietroCoppola/newsfetch/internal/fetch"
)

// TickerMarker selects the symbol that prefixes each non-hero entry in a
// multi-story render. The set is intentionally small: each option carries a
// different visual signal and the wizard exposes them as user-facing
// choices, so the menu has to stay short enough to be a tasteful default
// rather than a paint-store of options.
type TickerMarker string

const (
	// TickerDot is the default. Neutral bullet, no suggested relationship
	// between hero and ticker beyond "more items".
	TickerDot TickerMarker = "dot"
	// TickerArrow signals continuation: "and also these".
	TickerArrow TickerMarker = "arrow"
	// TickerBranch draws a tree, signalling that ticker entries are
	// children of the same render. The boxed renderer adds a connecting
	// ┬ on the divider; the plain renderer adds one on the hero's
	// bottom edge so the spine reads as continuous.
	TickerBranch TickerMarker = "branch"
)

// KnownTickerMarkers is the single source of truth for the supported
// marker names. The config validator and the wizard both consume it, so
// adding a marker means one new entry here plus a case in
// markerSymbol.
var KnownTickerMarkers = []TickerMarker{TickerDot, TickerArrow, TickerBranch}

// MultiOptions configures a [Multi] render.
type MultiOptions struct {
	Marker TickerMarker
	Boxed  bool
}

// Multi renders a hero+ticker layout: the first story as a [Boxed] panel,
// the rest as one-line ticker entries. width sets the boxed hero's width
// and the ticker line budget. now is the reference time for relative ages.
//
// On a single-story slice Multi delegates to [Boxed] (no tickers, no
// marker decisions to make). On an empty slice it PANICS — the renderer
// is downstream of selection, which guarantees at least one story by the
// time we get here.
//
// Ticker line content: "title — host (age)". Author is intentionally
// omitted; ticker lines are glanceable subordinate context, not full
// attribution. When the line exceeds budget the title is truncated rather
// than the suffix — losing host and age would strip the line of the
// metadata that makes a single glance useful.
//
// Display-column-aware truncation (CJK/emoji width) is a polish item
// targeted for a later width sweep; this function uses the same
// rune-count rule as [Boxed].
func Multi(stories []fetch.Story, now time.Time, width int, opts MultiOptions) string {
	if len(stories) == 0 {
		panic("render.Multi: stories must be non-empty")
	}
	if len(stories) == 1 {
		return Boxed(stories[0], now, width)
	}
	if opts.Boxed {
		return renderMultiBoxed(stories, now, width, opts.Marker)
	}
	return renderMultiPlain(stories, now, width, opts.Marker)
}

// renderMultiPlain renders the hero in its own box with ticker lines below.
// Tickers are indented two columns so their content sits where the hero's
// content sits (one box-edge column plus one pad column). Branch markers
// add a ┬ on the hero's bottom edge so the spine reads as continuous.
func renderMultiPlain(stories []fetch.Story, now time.Time, width int, marker TickerMarker) string {
	var b strings.Builder
	b.WriteString(heroBox(stories[0], now, width, marker == TickerBranch))
	tickers := stories[1:]
	for i, s := range tickers {
		mk := markerSymbol(marker, i, len(tickers))
		budget := width - 2 - utf8.RuneCountInString(mk)
		if budget < minWidth {
			budget = minWidth
		}
		b.WriteString("  " + mk + tickerBody(s, now, budget) + "\n")
	}
	return b.String()
}

// renderMultiBoxed renders one outer box: hero on top, a horizontal
// divider, then ticker rows inside the same box. Branch markers add a ┬
// on the divider above the spine column.
func renderMultiBoxed(stories []fetch.Story, now time.Time, width int, marker TickerMarker) string {
	contentW := width - 4
	if contentW < 1 {
		contentW = 1
	}
	horiz := strings.Repeat(boxHoriz, width-2)
	divider := boxLeftTee + horiz + boxRightTee
	if marker == TickerBranch {
		// Spine sits at content column 1 (full-row column 3). Tee one
		// column past the box edge to land directly above the spine.
		divider = boxLeftTee + boxHoriz + boxDownTee + strings.Repeat(boxHoriz, width-4) + boxRightTee
	}
	var b strings.Builder
	b.WriteString(boxTopLeft + horiz + boxTopRight + "\n")
	b.WriteString(boxVert + " " + padRight(truncate(stories[0].Title, contentW), contentW) + " " + boxVert + "\n")
	b.WriteString(boxVert + " " + padRight(truncate(metaLine(stories[0], now), contentW), contentW) + " " + boxVert + "\n")
	b.WriteString(divider + "\n")
	tickers := stories[1:]
	for i, s := range tickers {
		mk := markerSymbol(marker, i, len(tickers))
		body := tickerBody(s, now, contentW-utf8.RuneCountInString(mk))
		line := truncate(mk+body, contentW)
		b.WriteString(boxVert + " " + padRight(line, contentW) + " " + boxVert + "\n")
	}
	b.WriteString(boxBotLeft + horiz + boxBotRight + "\n")
	return b.String()
}

// heroBox renders the standard [Boxed] hero, optionally swapping its
// bottom-left corner for a ╰─┬ when a branch spine continues below.
func heroBox(s fetch.Story, now time.Time, width int, withSpine bool) string {
	if !withSpine {
		return Boxed(s, now, width)
	}
	contentW := width - 4
	if contentW < 1 {
		contentW = 1
	}
	horiz := strings.Repeat(boxHoriz, width-2)
	bottom := boxBotLeft + boxHoriz + boxDownTee + strings.Repeat(boxHoriz, width-4) + boxBotRight
	var b strings.Builder
	b.WriteString(boxTopLeft + horiz + boxTopRight + "\n")
	b.WriteString(boxVert + " " + padRight(truncate(s.Title, contentW), contentW) + " " + boxVert + "\n")
	b.WriteString(boxVert + " " + padRight(truncate(metaLine(s, now), contentW), contentW) + " " + boxVert + "\n")
	b.WriteString(bottom + "\n")
	return b.String()
}

func markerSymbol(m TickerMarker, i, total int) string {
	switch m {
	case TickerArrow:
		return "↳ "
	case TickerBranch:
		if i == total-1 {
			return "└─ "
		}
		return "├─ "
	default:
		return "· "
	}
}

// tickerBody builds one ticker entry within budget columns, preserving the
// host+age suffix and only truncating the title when needed. If the suffix
// alone overflows the budget (pathologically narrow), the whole line is
// truncated together.
func tickerBody(s fetch.Story, now time.Time, budget int) string {
	suffix := " — " + hostname(s.URL) + " (" + relativeAge(s.CreatedAt, now) + ")"
	suffixCols := utf8.RuneCountInString(suffix)
	if suffixCols >= budget-1 {
		return truncate(s.Title+suffix, budget)
	}
	return truncate(s.Title, budget-suffixCols) + suffix
}

// JSONMulti renders stories as a JSON array, one object per story, matching
// the [JSON] single-story shape per element. The trailing newline matches
// JSON's pipeline-friendly convention.
func JSONMulti(stories []fetch.Story, now time.Time) string {
	type payload struct {
		Title      string   `json:"title"`
		URL        string   `json:"url"`
		Source     string   `json:"source"`
		AgeSeconds int64    `json:"age_seconds"`
		Tags       []string `json:"tags"`
	}
	out := make([]payload, len(stories))
	for i, s := range stories {
		tags := s.Tags
		if tags == nil {
			tags = []string{}
		}
		ageSeconds := int64(now.Sub(s.CreatedAt).Seconds())
		if ageSeconds < 0 {
			ageSeconds = 0
		}
		out[i] = payload{
			Title:      s.Title,
			URL:        s.URL,
			Source:     s.Source,
			AgeSeconds: ageSeconds,
			Tags:       tags,
		}
	}
	b, _ := json.Marshal(out)
	return string(b) + "\n"
}
