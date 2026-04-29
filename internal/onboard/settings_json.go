package onboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ReadSettingsJSON parses [Answers] from r as JSON for --settings. Schema:
//
//	{ "topics": ["rust"], "style": "boxed", "sources": ["hackernews"], "count": 3,
//	  "ticker_marker": "branch", "ticker_boxed": true }
//
// topics, style, sources, and count are required — --settings is the
// edit-everything contract, in contrast with --init's onboarding contract
// where most fields are optional. count must be in [1, MaxCount].
//
// ticker_marker and ticker_boxed are OPTIONAL: the settings wizard hides
// them when style != "boxed" or count <= 1, and the JSON contract mirrors
// that. When omitted, the values from current are preserved verbatim — a
// user who switches style=boxed → minimal and back through scripted edits
// finds their prior ticker tuning intact, matching the wizard's persist-
// silently behaviour. Unknown JSON fields are rejected.
func ReadSettingsJSON(r io.Reader, current Answers) (Answers, error) {
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
		return Answers{}, fmt.Errorf("decode --settings JSON: %w", err)
	}
	if raw.Topics == nil {
		return Answers{}, errors.New(`--settings JSON: missing required field "topics" (array of strings; [] is allowed)`)
	}
	if raw.Style == nil {
		return Answers{}, errors.New(`--settings JSON: missing required field "style" (boxed | minimal | json)`)
	}
	if raw.Sources == nil {
		return Answers{}, errors.New(`--settings JSON: missing required field "sources" (non-empty array of source names)`)
	}
	if raw.Count == nil {
		return Answers{}, errors.New(`--settings JSON: missing required field "count" (1..4)`)
	}
	if err := validateStyle("--settings", *raw.Style); err != nil {
		return Answers{}, err
	}
	if err := validateSources("--settings", *raw.Sources); err != nil {
		return Answers{}, err
	}
	if err := validateCount("--settings", *raw.Count); err != nil {
		return Answers{}, err
	}
	a := Answers{
		Topics:       *raw.Topics,
		Style:        *raw.Style,
		Sources:      *raw.Sources,
		Count:        *raw.Count,
		TickerMarker: current.TickerMarker,
		TickerBoxed:  current.TickerBoxed,
	}
	if raw.TickerMarker != nil {
		if err := validateTickerMarker("--settings", *raw.TickerMarker); err != nil {
			return Answers{}, err
		}
		a.TickerMarker = *raw.TickerMarker
	}
	if raw.TickerBoxed != nil {
		a.TickerBoxed = *raw.TickerBoxed
	}
	return a, nil
}
