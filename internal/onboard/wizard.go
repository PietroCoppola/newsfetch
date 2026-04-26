package onboard

import (
	"errors"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"

	"github.com/PietroCoppola/newsfetch/internal/fetch"
)

// topicOptions defines the topic multi-select choices used by both wizards.
// Kept as a package-level slice so --init and --settings present an
// identical menu — adding or removing a topic is a one-line change.
var topicOptions = []huh.Option[string]{
	huh.NewOption("AI / LLMs", "ai"),
	huh.NewOption("Rust", "rust"),
	huh.NewOption("Go", "go"),
	huh.NewOption("Python", "python"),
	huh.NewOption("JavaScript / TypeScript", "javascript"),
	huh.NewOption("Databases", "databases"),
	huh.NewOption("Security", "security"),
	huh.NewOption("Systems / OS / kernels", "systems"),
	huh.NewOption("DevOps / infrastructure", "devops"),
	huh.NewOption("Hardware", "hardware"),
}

// styleOptions defines the display-style picker choices, used by both wizards.
var styleOptions = []huh.Option[string]{
	huh.NewOption("Boxed (framed, default)", "boxed"),
	huh.NewOption("Minimal (one line)", "minimal"),
	huh.NewOption("JSON (machine-readable)", "json"),
}

// sourceOptions builds the source multi-select choices from the canonical
// list in fetch.KnownSourceNames so a new source automatically shows up
// in the --settings wizard without a second edit.
func sourceOptions() []huh.Option[string] {
	opts := make([]huh.Option[string], 0, len(fetch.KnownSourceNames))
	for _, name := range fetch.KnownSourceNames {
		label := name
		switch name {
		case "hackernews":
			label = "Hacker News"
		case "lobsters":
			label = "Lobste.rs"
		}
		opts = append(opts, huh.NewOption(label, name))
	}
	return opts
}

// Answers captures wizard / JSON-stdin output for both --init and --settings.
// Sources is nil-vs-non-nil sensitive: nil means "the caller did not specify
// sources" (config writers omit the field so future default changes flow
// through), non-nil means "use exactly these" (config writers emit the line).
type Answers struct {
	Topics  []string
	Style   string
	Sources []string // nil → omit from config; non-nil → emit verbatim
}

// RunInitWizard drives the interactive --init UI: a topic multi-select
// followed by a display-style picker. Sources is intentionally not surfaced —
// --init is the opinionated onboarding contract; users opt into source
// selection via --settings or the JSON-stdin power-user path. Returns the
// user's choices with Sources unset (nil). Not unit-tested — the TUI is
// exercised via manual smoke.
func RunInitWizard() (Answers, error) {
	var a Answers
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Pick topics that interest you").
				Description("These bias which stories surface. Leave empty to see whatever's hot.").
				Filterable(false).
				Options(topicOptions...).
				Value(&a.Topics),
			huh.NewSelect[string]().
				Title("Display style").
				Filtering(false).
				Options(styleOptions...).
				Value(&a.Style),
		),
	).WithKeyMap(initKeyMap())
	if err := form.Run(); err != nil {
		return Answers{}, err
	}
	return a, nil
}

// RunSettingsWizard drives the interactive --settings UI: a topic multi-
// select pre-checked with current.Topics, a style picker pre-selected to
// current.Style, and a source multi-select pre-checked with current.Sources.
// The sources field validates non-empty inline so the user can't save a
// configuration that would trigger the next-run "sources is empty" warning.
//
// Returns the user's edited choices; Answers.Sources is always non-nil
// (sources is required and validated). Not unit-tested — manual smoke.
func RunSettingsWizard(current Answers) (Answers, error) {
	a := Answers{
		Topics:  append([]string(nil), current.Topics...),
		Style:   current.Style,
		Sources: append([]string(nil), current.Sources...),
	}
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Topics").
				Description("These bias which stories surface. Leave empty to see whatever's hot.").
				Filterable(false).
				Options(topicOptions...).
				Value(&a.Topics),
			huh.NewSelect[string]().
				Title("Display style").
				Filtering(false).
				Options(styleOptions...).
				Value(&a.Style),
			huh.NewMultiSelect[string]().
				Title("Sources").
				Description("Where to fetch news from. At least one required.").
				Filterable(false).
				Options(sourceOptions()...).
				Validate(func(v []string) error {
					if len(v) == 0 {
						return errors.New("pick at least one source")
					}
					return nil
				}).
				Value(&a.Sources),
		),
	).WithKeyMap(settingsKeyMap())
	if err := form.Run(); err != nil {
		return Answers{}, err
	}
	return a, nil
}

// initKeyMap is tuned for the 2-field --init wizard so tab cycles between
// topics and style:
//
//   - Toggle help shows "space/x" (both work; default surfaced only x).
//   - Field 1 (topics multi-select): tab forward, no back (Prev disabled).
//   - Field 2 (style select): tab back, enter submit. Mashing tab pings
//     between the two fields without a shift modifier.
//   - SelectAll bound to "a" (default ctrl+a feels overkill).
func initKeyMap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()

	km.MultiSelect.Toggle = key.NewBinding(key.WithKeys(" ", "x"), key.WithHelp("space/x", "toggle"))
	km.MultiSelect.Prev = key.NewBinding(key.WithDisabled())
	km.MultiSelect.Next = key.NewBinding(key.WithKeys("enter", "tab"), key.WithHelp("tab/enter", "next"))
	km.MultiSelect.SelectAll = key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "select all"))

	km.Select.Prev = key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "back"))
	km.Select.Next = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "submit"))

	return km
}

// settingsKeyMap is tuned for the 3-field --settings wizard. Standard
// tab/shift+tab navigation since the "tab cycles between two fields"
// trick from --init doesn't apply cleanly to three fields. Other huh
// defaults are kept; only the affordances --init customised stay
// customised so the two wizards feel consistent.
func settingsKeyMap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()

	km.MultiSelect.Toggle = key.NewBinding(key.WithKeys(" ", "x"), key.WithHelp("space/x", "toggle"))
	km.MultiSelect.SelectAll = key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "select all"))
	// Prev and Next stay at huh defaults: shift+tab back, tab/enter forward.

	return km
}
