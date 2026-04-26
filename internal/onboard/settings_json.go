package onboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ReadSettingsJSON parses [Answers] from r as JSON for --settings. Schema:
//
//	{ "topics": ["rust"], "style": "boxed", "sources": ["hackernews", "lobsters"] }
//
// All three fields are required, including sources — --settings is the
// edit-everything contract, in contrast with --init's onboarding contract
// where sources is optional. sources must be a non-empty list of names from
// fetch.KnownSourceNames. Unknown JSON fields are rejected.
func ReadSettingsJSON(r io.Reader) (Answers, error) {
	var raw struct {
		Topics  *[]string `json:"topics"`
		Style   *string   `json:"style"`
		Sources *[]string `json:"sources"`
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
	if err := validateStyle("--settings", *raw.Style); err != nil {
		return Answers{}, err
	}
	if err := validateSources("--settings", *raw.Sources); err != nil {
		return Answers{}, err
	}
	return Answers{
		Topics:  *raw.Topics,
		Style:   *raw.Style,
		Sources: *raw.Sources,
	}, nil
}
