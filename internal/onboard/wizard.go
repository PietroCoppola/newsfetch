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

// countOptions defines the per-render story-count picker, surfaced in the
// settings wizard. Capped at defaults.MaxCount; values above turn hero+ticker
// into a list, which the spec deliberately rejects. Labels are kept tight
// for inline (single-row) display.
var countOptions = []huh.Option[int]{
	huh.NewOption("1", 1),
	huh.NewOption("2", 2),
	huh.NewOption("3", 3),
	huh.NewOption("4", 4),
}

// tickerMarkerOptions defines the ticker-marker picker. Names mirror
// render.KnownTickerMarkers; the labels carry a one-glyph preview so the
// user can tell them apart without remembering what each name draws.
var tickerMarkerOptions = []huh.Option[string]{
	huh.NewOption("Dot · (default, neutral)", "dot"),
	huh.NewOption("Arrow ↳ (continuation)", "arrow"),
	huh.NewOption("Branch ├─ (tree)", "branch"),
}

// tickerBoxedOptions defines the box-style picker for multi-story renders.
var tickerBoxedOptions = []huh.Option[bool]{
	huh.NewOption("Plain (hero box, ticker lines beneath)", false),
	huh.NewOption("Connected (one outer box around hero plus ticker)", true),
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
//
// Sources is nil-vs-non-nil sensitive: nil means "the caller did not specify
// sources" (config writers omit the field so future default changes flow
// through), non-nil means "use exactly these" (config writers emit the line).
//
// Count, TickerMarker, and TickerBoxed are persisted unconditionally even
// when currently inert (e.g. TickerMarker survives a switch from
// style=boxed to style=minimal). The choice is deliberate: a user who
// previously tuned the multi-story render expects to find that tuning
// preserved when they switch back, rather than having to re-pick from
// defaults. The settings wizard mirrors this by hiding the ticker fields
// when inert rather than clearing them.
type Answers struct {
	Topics       []string
	Style        string
	Sources      []string // nil → omit from config; non-nil → emit verbatim
	Count        int
	TickerMarker string
	TickerBoxed  bool
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
		Topics:       append([]string(nil), current.Topics...),
		Style:        current.Style,
		Sources:      append([]string(nil), current.Sources...),
		Count:        current.Count,
		TickerMarker: current.TickerMarker,
		TickerBoxed:  current.TickerBoxed,
	}
	form := huh.NewForm(
		// Group 1: always shown. Content config first (topics + sources):
		// what news, from where. Presentation config (style + count) last.
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Topics").
				Description("These bias which stories surface. Leave empty to see whatever's hot.").
				Filterable(false).
				Options(topicOptions...).
				Value(&a.Topics),
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
			huh.NewSelect[string]().
				Title("Display style").
				Filtering(false).
				Options(styleOptions...).
				Value(&a.Style),
			huh.NewSelect[int]().
				Title("Stories per render").
				Description("How many stories appear each invocation.").
				Filtering(false).
				Inline(true).
				Options(countOptions...).
				Value(&a.Count),
		),
		// Group 2: only relevant when more than one story renders inside a
		// boxed hero. Hidden otherwise so the wizard stays tight in the
		// common case (single-story or non-boxed style). Hidden values are
		// preserved, not cleared — a user who switches away and back finds
		// their prior tuning intact.
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Ticker marker").
				Description("Symbol prefixing each non-hero story.").
				Filtering(false).
				Options(tickerMarkerOptions...).
				Value(&a.TickerMarker),
			huh.NewSelect[bool]().
				Title("Ticker box style").
				Filtering(false).
				Options(tickerBoxedOptions...).
				Value(&a.TickerBoxed),
		).WithHideFunc(func() bool {
			return a.Style != "boxed" || a.Count <= 1
		}),
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
// tab/shift+tab navigation: huh doesn't expose a clean way to make tab
// on the last field cycle back to the first (the public KeyMap is
// form-level, not per-field, and Next/Submit on the last field can't
// be redirected without forking huh).
//
// The bindings below exist primarily to make the footer help text
// consistent across all three fields. By default huh shows different
// labels per field type ("enter confirm" on topics, "enter select" on
// style, "enter submit" on sources) and leaves the filter binding
// visible even when Filtering/Filterable is off. We override them so a
// user reading the footer sees the same vocabulary regardless of which
// field has focus.
func settingsKeyMap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()

	km.MultiSelect.Toggle = key.NewBinding(key.WithKeys(" ", "x"), key.WithHelp("space/x", "toggle"))
	km.MultiSelect.SelectAll = key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "select all"))
	km.MultiSelect.Prev = key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "back"))
	km.MultiSelect.Next = key.NewBinding(key.WithKeys("tab", "enter"), key.WithHelp("tab/enter", "next"))
	// Submit is enter-only on purpose. Binding tab here would be a footgun:
	// a user expecting tab to cycle (impossible with huh) would accidentally
	// submit the form on the last field. Tab on the last field does nothing
	// (Next is invalid past the last field), which is safer than surprise
	// submit; enter is the explicit submit gesture, surfaced in the footer.
	km.MultiSelect.Submit = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "submit"))
	km.MultiSelect.Filter = key.NewBinding(key.WithDisabled())

	km.Select.Prev = key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "back"))
	km.Select.Next = key.NewBinding(key.WithKeys("tab", "enter"), key.WithHelp("tab/enter", "next"))
	km.Select.Submit = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "submit"))
	km.Select.Filter = key.NewBinding(key.WithDisabled())

	return km
}
