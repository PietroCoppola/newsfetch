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
	"github.com/PietroCoppola/newsfetch/internal/history"
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
	seen := loadSeen(cfg, now, errOut)
	f, readErr := cache.Read(path)
	if readErr == nil && len(f.Stories) > 0 {
		picked := selectFromPool(f.Stories, seen, cfg, now, rng)
		writeStories(out, picked, cfg, now)
		recordHistory(picked, now, errOut)
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
	picked := selectFromPool(stories, seen, cfg, now, rng)
	writeStories(out, picked, cfg, now)
	recordHistory(picked, now, errOut)
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

// loadSeen returns the user's render history as a hash set for pre-filter,
// time-gated to entries within cfg.DedupWindow of now. Older entries age
// out of the dedup pool and become eligible for re-rendering. A
// DedupWindow of zero disables the time gate entirely (no dedup, every
// cached story is always eligible).
//
// A read error (corrupt file, unreadable) is logged to errOut and treated
// as empty history — failing to dedup is strictly better than failing to
// render. A missing file is the normal first-run case and produces no log.
func loadSeen(cfg config.Config, now time.Time, errOut io.Writer) map[string]struct{} {
	if cfg.DedupWindow <= 0 {
		return map[string]struct{}{}
	}
	path, err := history.Path()
	if err != nil {
		fmt.Fprintln(errOut, "newsfetch: warning: history path:", err)
		return map[string]struct{}{}
	}
	f, err := history.Read(path)
	if err != nil {
		fmt.Fprintln(errOut, "newsfetch: warning: history read:", err)
		return map[string]struct{}{}
	}
	return f.RecentHashSet(now, cfg.DedupWindow)
}

// selectFromPool pre-filters pool against seen, then picks cfg.Count
// stories with diversity-aware multi-selection. If every story in the
// pool has been seen, the filter is bypassed so the user gets a render
// rather than the offline fallback — eventual repeats beat eventual
// silence.
func selectFromPool(pool []fetch.Story, seen map[string]struct{}, cfg config.Config, now time.Time, rng *rand.Rand) []fetch.Story {
	candidates := rank.Filter(pool, seen)
	if len(candidates) == 0 {
		candidates = pool
	}
	return rank.SelectN(candidates, cfg.Count, rank.Options{
		Topics:   cfg.Topics,
		Now:      now,
		PoolSize: defaults.RankPoolSize,
	}, rng)
}

// recordHistory appends the rendered stories to seen.json in render order
// (hero first, then ticker entries). Write failures are logged but do not
// fail the render — losing one entry to a transient write error matters
// less than the user's terminal opening cleanly.
func recordHistory(rendered []fetch.Story, now time.Time, errOut io.Writer) {
	if len(rendered) == 0 {
		return
	}
	path, err := history.Path()
	if err != nil {
		fmt.Fprintln(errOut, "newsfetch: warning: history path:", err)
		return
	}
	entries := make([]history.Entry, len(rendered))
	for i, s := range rendered {
		entries[i] = history.Entry{
			Hash:       s.Hash(),
			Title:      s.Title,
			URL:        s.URL,
			Source:     s.Source,
			Tags:       s.Tags,
			RenderedAt: now,
		}
	}
	if err := history.Append(path, entries); err != nil {
		fmt.Fprintln(errOut, "newsfetch: warning: history append:", err)
	}
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
	// countFlag is sentinel-zero so we can distinguish "user didn't pass
	// --count" (keep cfg.Count from config) from "user passed --count=0"
	// (clamped + warned by the validator). flag.IntVar with default -1
	// gives the same effect with a real integer.
	countFlag := fs.Int("count", -1, "stories to render this invocation: 1..4 (overrides config)")
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
	if cfg.Count != config.Defaults().Count {
		src.Count = "config"
	}
	if *countFlag != -1 {
		cfg.Count = *countFlag
		src.Count = "flag"
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
  --count=<n>       number of stories this render: 1..4 (default 1)

Subcommands:
  --init            interactive setup: pick topics, style, patch shell rc
                    if stdin is not a TTY, reads JSON instead:
                      {"topics": ["rust"], "style": "boxed"}
                      sources, count, ticker_marker, ticker_boxed are optional
  --settings        edit existing config: topics, style, sources, count, ticker
                    if stdin is not a TTY, reads JSON instead:
                      {"topics": ["rust"], "style": "boxed",
                       "sources": ["hackernews"], "count": 1}
                      first four required; ticker_marker, ticker_boxed optional
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

// writeStories dispatches to the renderer named by cfg.Style for one or
// more stories. The caller has already validated cfg fields; any unknown
// style falls back to boxed (belt-and-suspenders).
//
// Per-style multi-story behaviour:
//
//   - boxed:   render.Multi handles single-story (delegates to Boxed) and
//     multi-story (hero + ticker) uniformly.
//   - minimal: N stacked minimal lines (literal repetition, no decoration).
//   - json:    one JSON object when len==1, a JSON array when len>1, so
//     existing single-story scripted consumers stay unbroken.
func writeStories(out io.Writer, stories []fetch.Story, cfg config.Config, now time.Time) {
	if len(stories) == 0 {
		return
	}
	switch cfg.Style {
	case "minimal":
		for _, s := range stories {
			fmt.Fprint(out, render.Minimal(s, now))
		}
	case "json":
		if len(stories) == 1 {
			fmt.Fprint(out, render.JSON(stories[0], now))
			return
		}
		fmt.Fprint(out, render.JSONMulti(stories, now))
	default:
		fmt.Fprint(out, render.Multi(stories, now, defaults.TermWidth(defaults.BoxWidth), render.MultiOptions{
			Marker: render.TickerMarker(cfg.TickerMarker),
			Boxed:  cfg.TickerBoxed,
		}))
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
