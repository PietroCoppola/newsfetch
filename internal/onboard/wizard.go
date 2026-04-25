package onboard

import (
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"
)

// Answers captures the wizard's output. Kept as a plain struct so tests can
// build canned answers without invoking the TUI.
type Answers struct {
	Topics []string
	Style  string
}

// RunWizard drives the interactive --init UI: a topic multi-select followed
// by a display-style picker. Returns the user's choices (or huh.ErrUserAborted
// if they cancel). Not unit-tested — the TUI is exercised via manual smoke.
func RunWizard() (Answers, error) {
	var a Answers
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Pick topics that interest you").
				Description("These bias which stories surface. Leave empty to see whatever's hot.").
				Filterable(false).
				Options(
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
				).
				Value(&a.Topics),
			huh.NewSelect[string]().
				Title("Display style").
				Filtering(false).
				Options(
					huh.NewOption("Boxed (framed, default)", "boxed"),
					huh.NewOption("Minimal (one line)", "minimal"),
					huh.NewOption("JSON (machine-readable)", "json"),
				).
				Value(&a.Style),
		),
	).WithKeyMap(wizardKeyMap())
	if err := form.Run(); err != nil {
		return Answers{}, err
	}
	return a, nil
}

// wizardKeyMap takes huh's defaults and tweaks the bindings to match the
// shape of this specific wizard:
//
//   - Toggle help shows "space" (space and x both still work; the default
//     footer prominently displayed "x" which surprised users).
//   - Tab moves back; enter moves forward / confirms. Single-key navigation
//     for both directions, no shift modifier.
//   - SelectAll is bound to "a" (was ctrl+a). Filter is off so plain "a" is
//     unambiguous in the multi-select.
func wizardKeyMap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()

	// Field-1 (multi-select): tab moves forward. Prev is unbound — there's
	// no field to go back to, so showing shift+tab in the help would just
	// be noise.
	km.MultiSelect.Toggle = key.NewBinding(key.WithKeys(" ", "x"), key.WithHelp("space/x", "toggle"))
	km.MultiSelect.Prev = key.NewBinding(key.WithDisabled())
	km.MultiSelect.Next = key.NewBinding(key.WithKeys("enter", "tab"), key.WithHelp("tab/enter", "next"))
	km.MultiSelect.SelectAll = key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "select all"))

	// Field-2 (select, last): tab moves backward so the user can mash tab to
	// cycle between the two fields. enter is the explicit submit — pressing
	// it here finishes the wizard.
	km.Select.Prev = key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "back"))
	km.Select.Next = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "submit"))

	return km
}
