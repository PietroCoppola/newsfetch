package onboard

import "github.com/charmbracelet/huh"

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
				Options(
					huh.NewOption("Boxed (framed, default)", "boxed"),
					huh.NewOption("Minimal (one line)", "minimal"),
					huh.NewOption("JSON (machine-readable)", "json"),
				).
				Value(&a.Style),
		),
	)
	if err := form.Run(); err != nil {
		return Answers{}, err
	}
	return a, nil
}
