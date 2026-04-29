// Command newsfetch renders one piece of bite-sized tech news each time a
// terminal opens. See spec.md at the repo root for the full design.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/PietroCoppola/newsfetch/internal/cache"
	"github.com/PietroCoppola/newsfetch/internal/config"
	"github.com/PietroCoppola/newsfetch/internal/defaults"
	"github.com/PietroCoppola/newsfetch/internal/fetch"
	"github.com/PietroCoppola/newsfetch/internal/onboard"
	"github.com/PietroCoppola/newsfetch/internal/rank"
	"github.com/PietroCoppola/newsfetch/internal/refreshlog"
	"github.com/PietroCoppola/newsfetch/internal/render"
)

const (
	refreshFlag   = "--__refresh"
	initFlag      = "--init"
	uninstallFlag = "--uninstall"
	settingsFlag  = "--settings"
)

// newSource returns the Source implementation for name. Tests MAY swap
// this to return httptest-backed sources, but MUST restore via
// t.Cleanup(func() { newSource = original }) to avoid leaking the swap
// into other tests. config.Validate guarantees only known names reach
// this function in production, so the default branch is defence in depth.
var newSource = func(name string) (fetch.Source, error) {
	switch name {
	case "hackernews":
		return &fetch.HackerNews{}, nil
	case "lobsters":
		return &fetch.Lobsters{}, nil
	default:
		return nil, fmt.Errorf("unknown source %q", name)
	}
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == refreshFlag {
		if err := runRefresh(); err != nil {
			_ = refreshlog.Append(err.Error())
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == initFlag {
		if err := runInit(os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, "newsfetch:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == uninstallFlag {
		if err := runUninstall(os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "newsfetch:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == settingsFlag {
		if err := runSettings(os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "newsfetch:", err)
			os.Exit(1)
		}
		return
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	if err := runDefault(os.Stdout, os.Stderr, os.Args[1:], rng); err != nil {
		fmt.Fprintln(os.Stderr, "newsfetch:", err)
		os.Exit(1)
	}
}

// runInit wires onboard.InitFlow to its production dependencies. The warm-
// cache step calls runDefault in-process — simpler than re-execing ourselves
// and avoids a second flag parse — but its output is suppressed (the user
// already sees install status; rendering a story on top would be noise).
//
// Answers source flips on stdin TTY detection: a real terminal gets the
// huh wizard; a pipe / redirect gets ReadInitJSON. Symmetric with
// --uninstall, which uses TTY detection to decide between interactive
// prompts and "do the obvious thing without asking".
func runInit(out, errOut io.Writer) error {
	return onboard.InitFlow(onboard.InitDeps{
		ConfigPath: config.Path,
		Shell:      onboard.Detect,
		Answers:    pickAnswerSource(os.Stdin),
		Out:        out,
		WarmCache: func() error {
			rng := rand.New(rand.NewSource(time.Now().UnixNano()))
			return runDefault(io.Discard, errOut, nil, rng)
		},
	})
}

// pickAnswerSource returns the function InitFlow will call to collect
// wizard answers. TTY → interactive huh wizard; non-TTY → JSON parsed
// from in. The non-TTY path makes scripted install possible:
//
//	echo '{"topics":["rust"],"style":"boxed"}' | newsfetch --init
func pickAnswerSource(in *os.File) func() (onboard.Answers, error) {
	if term.IsTerminal(int(in.Fd())) {
		return onboard.RunInitWizard
	}
	return func() (onboard.Answers, error) { return onboard.ReadInitJSON(in) }
}

// runSettings wires onboard.SettingsFlow to its production dependencies.
// Reads the existing config from disk (errors if missing — --settings is the
// edit-existing path, --init is the bootstrap path) and routes the answer
// collection through the wizard or JSON-stdin depending on TTY status.
func runSettings(out io.Writer) error {
	return onboard.SettingsFlow(onboard.SettingsDeps{
		ConfigPath: config.Path,
		Current: func(path string) (onboard.Answers, error) {
			cfg, err := config.Load(path)
			if err != nil {
				return onboard.Answers{}, err
			}
			return onboard.Answers{
				Topics:       cfg.Topics,
				Style:        cfg.Style,
				Sources:      cfg.Sources,
				Count:        cfg.Count,
				TickerMarker: cfg.TickerMarker,
				TickerBoxed:  cfg.TickerBoxed,
			}, nil
		},
		Answers: pickSettingsAnswerSource(os.Stdin),
		Out:     out,
	})
}

// pickSettingsAnswerSource returns the function SettingsFlow will call to
// collect updated answers. TTY → interactive wizard pre-filled with the
// caller-provided current values; non-TTY → JSON parsed from in, with the
// caller-provided current values used as fallback for fields the wizard
// would have hidden (ticker_marker, ticker_boxed) so omitted fields don't
// silently revert to defaults. Symmetric with --init's pickAnswerSource.
func pickSettingsAnswerSource(in *os.File) func(onboard.Answers) (onboard.Answers, error) {
	if term.IsTerminal(int(in.Fd())) {
		return onboard.RunSettingsWizard
	}
	return func(current onboard.Answers) (onboard.Answers, error) {
		return onboard.ReadSettingsJSON(in, current)
	}
}

// runUninstall removes the shell rc block and offers (interactively, when
// stdin is a TTY) to also remove the config and cache files. Non-interactive
// runs default to "no" so scripts that pipe newsfetch don't hang waiting for
// input. The rc block removal itself is unconditional — that's the user's
// stated intent by invoking --uninstall.
func runUninstall(out io.Writer) error {
	return onboard.UninstallFlow(onboard.UninstallDeps{
		ConfigPath: config.Path,
		CachePath:  cache.Path,
		Shell:      onboard.Detect,
		Out:        out,
		Confirm:    promptYesNo(os.Stdin, out),
	})
}

// promptYesNo returns a Confirm function for UninstallFlow. If in is a TTY,
// the user is asked y/N for each item. If in is not a TTY (script, pipe),
// the function returns true unconditionally — without an observer to ask,
// `--uninstall` is read literally as "remove everything". Leaving config and
// cache behind silently in that case is worse than removing them: the user
// would never see the "left in place" message and would just have orphaned
// files.
func promptYesNo(in *os.File, out io.Writer) func(string) bool {
	if !term.IsTerminal(int(in.Fd())) {
		return func(string) bool { return true }
	}
	reader := bufio.NewReader(in)
	return func(prompt string) bool {
		fmt.Fprintf(out, "%s [y/N] ", prompt)
		line, err := reader.ReadString('\n')
		if err != nil {
			return false
		}
		answer := strings.ToLower(strings.TrimSpace(line))
		return answer == "y" || answer == "yes"
	}
}

// runDefault is the hot path. It parses flags, loads and validates config,
// reads the cache, and prints a rendered story (or a fallback). Callers
// pass an rng so tests can seed determinism.
func runDefault(out, errOut io.Writer, args []string, rng *rand.Rand) error {
	cfg, earlyExit, err := parseAndLoad(args, errOut)
	if err != nil {
		return err
	}
	switch earlyExit {
	case exitVersion:
		fmt.Fprintln(out, defaults.Version)
		return nil
	case exitHelp:
		printHelp(out)
		return nil
	}

	path, err := cache.Path()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	f, readErr := cache.Read(path)
	if readErr == nil && len(f.Stories) > 0 {
		story := rank.Select(f.Stories, rank.Options{
			Topics:   cfg.Topics,
			Now:      now,
			PoolSize: defaults.RankPoolSize,
		}, rng)
		writeStory(out, story, cfg.Style, now)
		if !f.IsFresh(cfg.CacheTTL, now) {
			spawnRefresh()
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaults.FetchTimeout)
	defer cancel()
	stories, errs, err := multiFetch(ctx, cfg)
	if err != nil {
		return err
	}
	for name, e := range errs {
		_ = refreshlog.Append(fmt.Sprintf("%s: %s", name, e))
	}
	if len(stories) == 0 {
		fmt.Fprint(out, render.Fallback(fallbackMessage(cfg.Sources)))
		return nil
	}
	story := rank.Select(stories, rank.Options{
		Topics:   cfg.Topics,
		Now:      now,
		PoolSize: defaults.RankPoolSize,
	}, rng)
	writeStory(out, story, cfg.Style, now)
	// Full-replace on partial fetch: a failed source's prior stories drop
	// out of the cache rather than ghosting indefinitely. Self-healing on
	// the next fully-successful refresh. Time-bounded merge (per-story TTL
	// on top of cache TTL) is the right shape if the partial-outage UX
	// ever feels rough — defer until evidence demands it.
	if writeErr := writeCache(path, stories, time.Now().UTC()); writeErr != nil {
		fmt.Fprintln(errOut, "newsfetch: warning: could not write cache:", writeErr)
	}
	return nil
}

type earlyExitKind int

const (
	exitRun earlyExitKind = iota
	exitVersion
	exitHelp
)

// parseAndLoad handles the flag parse, config.Load, and config.Validate
// steps and returns the merged Config. On parse error, it emits a warning
// to errOut and returns Defaults(). On --version or --help, returns an
// early-exit marker so the caller can handle those without continuing.
func parseAndLoad(args []string, errOut io.Writer) (config.Config, earlyExitKind, error) {
	fs := flag.NewFlagSet("newsfetch", flag.ContinueOnError)
	fs.SetOutput(errOut)
	// Suppress stdlib's default usage dump on -h and bad flags; we print
	// printHelp from exitHelp and a single-line error from main.
	fs.Usage = func() {}
	styleFlag := fs.String("style", "", "display mode: boxed | minimal | json")
	topics := &topicsFlag{}
	fs.Var(topics, "topics", "comma-separated topic list (explicit empty defeats config)")
	showVersion := fs.Bool("version", false, "print version and exit")
	showHelp := fs.Bool("help", false, "print usage and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return config.Defaults(), exitHelp, nil
		}
		return config.Defaults(), exitRun, err
	}
	if *showVersion {
		return config.Defaults(), exitVersion, nil
	}
	if *showHelp {
		return config.Defaults(), exitHelp, nil
	}

	cfgPath, err := config.Path()
	if err != nil {
		return config.Defaults(), exitRun, nil
	}
	cfg, loadErr := config.Load(cfgPath)
	var src config.FieldSources
	// Parse error: emit one warning, use defaults, continue to apply flags.
	if loadErr != nil {
		fmt.Fprintf(errOut, "newsfetch: config: %s: %s; using defaults\n", cfgPath, loadErr)
		cfg = config.Defaults()
	}
	// Apply CLI flag overrides (always, even after a config parse error).
	if cfg.Style != config.Defaults().Style {
		src.Style = "config"
	}
	if *styleFlag != "" {
		cfg.Style = *styleFlag
		src.Style = "flag"
	}
	if topics.set {
		cfg.Topics = topics.vals
	}
	cfg = config.Validate(cfg, src, errOut)
	return cfg, exitRun, nil
}

func printHelp(out io.Writer) {
	fmt.Fprint(out, `Usage: newsfetch [flags]

Render one piece of tech news. Run without flags for the default boxed panel.

Per-render overrides (apply to this invocation only; config untouched):
  --style=<mode>    display mode for this render: boxed (default) | minimal | json
  --topics=<list>   topic bias for this render, comma-separated; '--topics=' defeats config

Subcommands:
  --init            interactive setup: pick topics, style, patch shell rc
                    if stdin is not a TTY, reads JSON instead:
                      {"topics": ["rust"], "style": "boxed"}
                      sources is optional in --init JSON
  --settings        edit existing config: topics, style, sources
                    if stdin is not a TTY, reads JSON instead:
                      {"topics": ["rust"], "style": "boxed", "sources": ["hackernews"]}
                      all three fields required
  --uninstall       remove the newsfetch block from your shell rc

  --version         print version and exit
  --help            print usage and exit
`)
}

func runRefresh() error {
	path, err := cache.Path()
	if err != nil {
		return err
	}
	cfg := config.Defaults()
	if cfgPath, err := config.Path(); err == nil {
		if loaded, err := config.Load(cfgPath); err == nil {
			cfg = loaded
		}
	}
	cfg = config.Validate(cfg, config.FieldSources{}, io.Discard)
	ctx, cancel := context.WithTimeout(context.Background(), defaults.FetchTimeout)
	defer cancel()
	stories, errs, err := multiFetch(ctx, cfg)
	if err != nil {
		return err
	}
	for name, e := range errs {
		_ = refreshlog.Append(fmt.Sprintf("%s: %s", name, e))
	}
	if len(stories) == 0 {
		return errors.New("all sources returned no stories")
	}
	return writeCache(path, stories, time.Now().UTC())
}

// fallbackMessage returns the user-facing string for the offline-render
// branch. With exactly one configured source, name it explicitly so the
// user knows which provider to investigate; with multiple, stay generic
// since blaming any one of them would be wrong.
//
// "Single source" here means singly-CONFIGURED, not the case where the
// user has multiple sources but only one is currently failing. M8 polish
// could distinguish — partial-failure messaging is one of the levers — but
// for M4 we treat configured-count as the signal because it's stable per
// invocation and doesn't need to peek at the errs map at the render site.
func fallbackMessage(sources []string) string {
	if len(sources) == 1 {
		return fmt.Sprintf("%s unavailable — check your connection", sources[0])
	}
	return defaults.FallbackMessage
}

// multiFetch instantiates each Source named in cfg.Sources and runs them in
// parallel via fetch.FetchAll. Per-source errors flow back as a name→err
// map; the caller decides whether to log them, surface to the user, or
// both. A factory error (unknown source name) is treated as fatal because
// config.Validate is supposed to filter those out before we get here — if
// one slips through, that's a bug worth surfacing rather than silently
// degrading.
func multiFetch(ctx context.Context, cfg config.Config) ([]fetch.Story, map[string]error, error) {
	sources := make([]fetch.Source, 0, len(cfg.Sources))
	for _, name := range cfg.Sources {
		src, err := newSource(name)
		if err != nil {
			return nil, nil, err
		}
		sources = append(sources, src)
	}
	stories, errs := fetch.FetchAll(ctx, sources, fetch.FetchOptions{
		MinPoints: cfg.MinPoints,
		Limit:     defaults.NumStories,
	})
	return stories, errs, nil
}

func writeCache(path string, stories []fetch.Story, at time.Time) error {
	return cache.Write(path, &cache.File{
		Version:         cache.SchemaVersion,
		CachedByVersion: defaults.Version,
		FetchedAt:       at,
		Stories:         stories,
	})
}

// writeStory dispatches to the renderer named by style. The caller has
// already validated style to one of the three known values; any other
// value falls back to boxed (belt-and-suspenders).
func writeStory(out io.Writer, s fetch.Story, style string, now time.Time) {
	switch style {
	case "minimal":
		fmt.Fprint(out, render.Minimal(s, now))
	case "json":
		fmt.Fprint(out, render.JSON(s, now))
	default:
		fmt.Fprint(out, render.Boxed(s, now, defaults.TermWidth(defaults.BoxWidth)))
	}
}

func spawnRefresh() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return
	}
	defer null.Close()
	cmd := exec.Command(exe, refreshFlag)
	cmd.Stdin = null
	cmd.Stdout = null
	cmd.Stderr = null
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return
	}
	_ = cmd.Process.Release()
}
