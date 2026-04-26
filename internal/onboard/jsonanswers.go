package onboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ReadJSONAnswers parses [Answers] from r as JSON. Used by --init when stdin
// is not a TTY so the install flow is scriptable without trying to render
// an interactive wizard into a pipe. Schema:
//
//	{ "topics": ["rust", "ai"], "style": "boxed" }
//
// Both fields are required. A missing field is an error rather than a
// silent default — a script should be explicit about what it's installing,
// and a half-specified config is harder to debug than a clean rejection.
// Unknown fields are rejected for the same reason.
func ReadJSONAnswers(r io.Reader) (Answers, error) {
	var raw struct {
		Topics *[]string `json:"topics"`
		Style  *string   `json:"style"`
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
	switch *raw.Style {
	case "boxed", "minimal", "json":
	default:
		return Answers{}, fmt.Errorf(`--init JSON: invalid style %q (must be boxed | minimal | json)`, *raw.Style)
	}
	return Answers{Topics: *raw.Topics, Style: *raw.Style}, nil
}
