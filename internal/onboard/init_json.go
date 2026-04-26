package onboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/PietroCoppola/newsfetch/internal/fetch"
)

// ReadInitJSON parses [Answers] from r as JSON. Used by --init when stdin
// is not a TTY so the install flow is scriptable without trying to render
// an interactive wizard into a pipe. Schema:
//
//	{ "topics": ["rust"], "style": "boxed" }                                         // basic
//	{ "topics": ["rust"], "style": "boxed", "sources": ["hackernews", "lobsters"] }  // power-user
//
// topics and style are required; a missing field is an error rather than a
// silent default — a script should be explicit about what it's installing,
// and a half-specified config is harder to debug than a clean rejection.
//
// sources is OPTIONAL on --init: --init's wizard intentionally doesn't ask
// for it, so JSON callers also get to skip it. When present, it must be a
// non-empty list of names from fetch.KnownSourceNames; when absent,
// Answers.Sources stays nil and the config writer omits the field so
// future default changes flow through. Unknown JSON fields are rejected.
func ReadInitJSON(r io.Reader) (Answers, error) {
	var raw struct {
		Topics  *[]string `json:"topics"`
		Style   *string   `json:"style"`
		Sources *[]string `json:"sources"`
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
	a := Answers{Topics: *raw.Topics, Style: *raw.Style}
	if raw.Sources != nil {
		if err := validateSources("--init", *raw.Sources); err != nil {
			return Answers{}, err
		}
		a.Sources = *raw.Sources
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
