package onboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/PietroCoppola/newsfetch/internal/defaults"
	"github.com/PietroCoppola/newsfetch/internal/fetch"
	"github.com/PietroCoppola/newsfetch/internal/render"
)

// ReadInitJSON parses [Answers] from r as JSON. Used by --init when stdin
// is not a TTY so the install flow is scriptable without trying to render
// an interactive wizard into a pipe. Schema:
//
//	{ "topics": ["rust"], "style": "boxed" }                                         // basic
//	{ "topics": ["rust"], "style": "boxed", "sources": ["hackernews", "lobsters"] }  // sources opt-in
//	{ "topics": ["rust"], "style": "boxed", "count": 3,
//	  "ticker_marker": "branch", "ticker_boxed": true }                              // multi-story tuning
//
// topics and style are required; a missing field is an error rather than a
// silent default — a script should be explicit about what it's installing,
// and a half-specified config is harder to debug than a clean rejection.
//
// sources, count, ticker_marker, and ticker_boxed are OPTIONAL on --init:
// the --init wizard intentionally surfaces only the basics, so JSON callers
// also get to skip the rest. When present they are validated; when absent
// they default to the compile-time defaults for everything except sources,
// which stays nil so the config writer omits it (future default changes
// flow through to the user). Unknown JSON fields are rejected.
func ReadInitJSON(r io.Reader) (Answers, error) {
	var raw struct {
		Topics       *[]string `json:"topics"`
		Style        *string   `json:"style"`
		Sources      *[]string `json:"sources"`
		Count        *int      `json:"count"`
		TickerMarker *string   `json:"ticker_marker"`
		TickerBoxed  *bool     `json:"ticker_boxed"`
	}
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return Answers{}, fmt.Errorf("decode --init JSON: %w", err)
	}
	if raw.Topics == nil {
		return Answers{}, errors.New(`--init JSON: missing required field "topics" (array of strings; [] is allowed)`)
	}
	if raw.Style == nil {
		return Answers{}, errors.New(`--init JSON: missing required field "style" (boxed | minimal | json)`)
	}
	if err := validateStyle("--init", *raw.Style); err != nil {
		return Answers{}, err
	}
	a := Answers{
		Topics:       *raw.Topics,
		Style:        *raw.Style,
		Count:        defaults.Count,
		TickerMarker: defaults.TickerMarker,
		TickerBoxed:  defaults.TickerBoxed,
	}
	if raw.Sources != nil {
		if err := validateSources("--init", *raw.Sources); err != nil {
			return Answers{}, err
		}
		a.Sources = *raw.Sources
	}
	if raw.Count != nil {
		if err := validateCount("--init", *raw.Count); err != nil {
			return Answers{}, err
		}
		a.Count = *raw.Count
	}
	if raw.TickerMarker != nil {
		if err := validateTickerMarker("--init", *raw.TickerMarker); err != nil {
			return Answers{}, err
		}
		a.TickerMarker = *raw.TickerMarker
	}
	if raw.TickerBoxed != nil {
		a.TickerBoxed = *raw.TickerBoxed
	}
	return a, nil
}

// validateStyle rejects style values outside the known set. Shared between
// the --init and --settings JSON readers so the error message format is
// consistent.
func validateStyle(flag, style string) error {
	switch style {
	case "boxed", "minimal", "json":
		return nil
	default:
		return fmt.Errorf(`%s JSON: invalid style %q (must be boxed | minimal | json)`, flag, style)
	}
}

// validateSources rejects empty lists and unknown source names. Mirrors
// the guarantees config.Validate gives at config-load time, but enforces
// them at JSON-parse time so a scripted user gets fail-loud feedback
// rather than a warning + fallback at the next render.
func validateSources(flag string, names []string) error {
	if len(names) == 0 {
		return fmt.Errorf("%s JSON: sources, if provided, must be non-empty", flag)
	}
	for _, n := range names {
		if !knownSourceName(n) {
			return fmt.Errorf("%s JSON: unknown source %q (valid: %v)", flag, n, fetch.KnownSourceNames)
		}
	}
	return nil
}

func knownSourceName(name string) bool {
	for _, k := range fetch.KnownSourceNames {
		if k == name {
			return true
		}
	}
	return false
}

// validateCount rejects out-of-range values. Mirrors config.Validate's
// clamp-and-warn but at JSON-parse time so scripted users get fail-loud
// feedback instead of a clamped value at next render.
func validateCount(flag string, n int) error {
	if n < 1 || n > defaults.MaxCount {
		return fmt.Errorf("%s JSON: count=%d out of [1, %d]", flag, n, defaults.MaxCount)
	}
	return nil
}

// validateTickerMarker rejects unknown marker names. The known set is
// owned by render.KnownTickerMarkers — single source of truth.
func validateTickerMarker(flag, name string) error {
	for _, m := range render.KnownTickerMarkers {
		if string(m) == name {
			return nil
		}
	}
	known := make([]string, len(render.KnownTickerMarkers))
	for i, m := range render.KnownTickerMarkers {
		known[i] = string(m)
	}
	return fmt.Errorf("%s JSON: unknown ticker_marker %q (valid: %v)", flag, name, known)
}
