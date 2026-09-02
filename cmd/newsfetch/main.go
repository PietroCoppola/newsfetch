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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/PietroCoppola/newsfetch/internal/cache"
	"github.com/PietroCoppola/newsfetch/internal/config"
	"github.com/PietroCoppola/newsfetch/internal/defaults"
	"github.com/PietroCoppola/newsfetch/internal/fetch"
	"github.com/PietroCoppola/newsfetch/internal/history"
	"github.com/PietroCoppola/newsfetch/internal/lockfile"
	"github.com/PietroCoppola/newsfetch/internal/onboard"
	"github.com/PietroCoppola/newsfetch/internal/refreshlog"
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
//
// Sanctioned exception to the no-global-mutable-state convention
// (CLAUDE.md, ruled 2026-08-26): package-main test seams, swapped only in
// tests, restored via t.Cleanup.
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
				Sources:      cfg.News.Aggregators,
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

// runDefault is the hot path. It parses flags, loads and validates
// config, assembles every enabled pool from its own cache, and prints the
// stacked render (or a fallback). Callers pass an rng so tests can seed
// determinism.
func runDefault(out, errOut io.Writer, args []string, rng *rand.Rand) error {
	cfg, cli, earlyExit, err := parseAndLoad(args, errOut)
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

	if cfg.Style == "statusline" {
		return runStatusline(out, errOut, cfg, cli, rng)
	}

	// One clock reading for the whole invocation, so every pool's relative
	// ages, the dedup gate and the history entries all agree.
	now := time.Now().UTC()
	seen := loadSeen(cfg, now, errOut)
	pools, rendered, err := assemblePools(cfg, seen, now, rng, errOut)
	if err != nil {
		return err
	}
	// Every active pool coming up empty — cold caches with a failed cold
	// fetch, or nothing left after the two-pass selection — is not
	// short-circuited here. writePools owns it, because what "nothing to
	// show" looks like depends on the style: a fallback sentence for
	// boxed and minimal, an empty array for json (R-3). recordHistory
	// no-ops on an empty render, so it needs no guard of its own.
	if err := writePools(out, pools, cfg, now); err != nil {
		return err
	}
	recordHistory(rendered, now, errOut)
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

// cliOverrides carries CLI-only flags that parseAndLoad returns alongside
// Config; see the pin/maxWidth flag declarations below for why they don't
// live on Config itself.
type cliOverrides struct {
	pin      string
	maxWidth int
}

// parseAndLoad handles the flag parse, config.Load, and config.Validate
// steps and returns the merged Config plus any CLI-only overrides. On parse
// error, it emits a warning to errOut and returns Defaults(). On --version
// or --help, returns an early-exit marker so the caller can handle those
// without continuing.
func parseAndLoad(args []string, errOut io.Writer) (config.Config, cliOverrides, earlyExitKind, error) {
	fs := flag.NewFlagSet("newsfetch", flag.ContinueOnError)
	fs.SetOutput(errOut)
	// Suppress stdlib's default usage dump on -h and bad flags; we print
	// printHelp from exitHelp and a single-line error from main.
	fs.Usage = func() {}
	styleFlag := fs.String("style", "", "display mode: boxed | minimal | json | statusline")
	topics := &topicsFlag{}
	fs.Var(topics, "topics", "comma-separated topic list (explicit empty defeats config)")
	// countFlag is sentinel-zero so we can distinguish "user didn't pass
	// --count" (keep cfg.Count from config) from "user passed --count=0"
	// (clamped + warned by the validator). flag.IntVar with default -1
	// gives the same effect with a real integer.
	countFlag := fs.Int("count", -1, "stories to render this invocation: 1..4 (overrides config)")
	// pin and maxWidth ride alongside Config rather than inside it: they are
	// per-invocation statusline parameters with no config-file counterpart,
	// so threading them through config.Validate would only add noise.
	pinFlag := fs.String("pin", "", "statusline style: pin story selection to this key (default: read stdin JSON)")
	maxWidthFlag := fs.Int("max-width", 0, "statusline style: max display columns, 0 = auto-detect")
	showVersion := fs.Bool("version", false, "print version and exit")
	showHelp := fs.Bool("help", false, "print usage and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return config.Defaults(), cliOverrides{}, exitHelp, nil
		}
		return config.Defaults(), cliOverrides{}, exitRun, err
	}
	if *showVersion {
		return config.Defaults(), cliOverrides{}, exitVersion, nil
	}
	if *showHelp {
		return config.Defaults(), cliOverrides{}, exitHelp, nil
	}

	cfgPath, err := config.Path()
	if err != nil {
		return config.Defaults(), cliOverrides{}, exitRun, nil
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
	cli := cliOverrides{pin: *pinFlag, maxWidth: *maxWidthFlag}
	if cli.maxWidth < 0 {
		cli.maxWidth = 0
	}
	cfg = config.Validate(cfg, src, errOut)
	return cfg, cli, exitRun, nil
}

func printHelp(out io.Writer) {
	fmt.Fprint(out, `Usage: newsfetch [flags]

Render one piece of tech news. Run without flags for the default boxed panel.

Per-render overrides (apply to this invocation only; config untouched):
  --style=<mode>    display mode for this render: boxed (default) | minimal | json | statusline
  --topics=<list>   topic bias for this render, comma-separated; '--topics=' defeats config
  --count=<n>       number of stories this render: 1..4 (default 1)
  --pin=<key>       statusline style: pin story selection to this key so
                    repeated renders stay stable; default reads prompt_id
                    (fallback session_id) from JSON on stdin
  --max-width=<n>   statusline style: truncate to n display columns
                    (default 80; detected terminal width when stdout is a TTY)

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

// runRefresh refreshes every enabled pool and rewrites their caches. It is
// single-flight across ALL pools: a cold statusline render and a multi-tab
// terminal restore can each spawn one, and a try-acquire on a sidecar
// refresh.lock (timeout 0 — exactly one attempt, no wait) makes the losers
// return quietly instead of hammering the same sources for the same answer.
// One process refreshes everything, so one lock is enough. The lock is held
// for the whole refresh and released when the process exits.
//
// Only contention is quiet. A lock that cannot be opened or flocked at all
// (unwritable cache dir, a filesystem without flock) is a real fault and
// propagates, so it reaches refreshlog instead of exiting 0 forever with
// nothing to show for it.
//
// Pools refresh SEQUENTIALLY, news first (ruling R-28): a detached process
// nobody waits on buys nothing from concurrency, and serial order keeps
// refresh.log readable. Failure is per pool (ruling R-29): every per-pool
// and per-feed problem is appended to refresh.log under a namespace prefix,
// a pool that fetched nothing simply does not write and keeps its stale
// cache, and this function returns an error — the process's exit 1 — only
// when EVERY ACTIVE pool failed. Active, not merely enabled: a pool R-35
// skips for having nothing configured inside it is never counted, so a
// Following-only user does not exit 1 on every refresh. That is a
// deliberate change to what a non-zero --__refresh exit means: it used to
// mean "no source produced stories", and now means "no pool did".
func runRefresh() error {
	dir, err := cache.Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	lock, err := lockfile.Acquire(filepath.Join(dir, "refresh.lock"), 0)
	if err != nil {
		if errors.Is(err, lockfile.ErrHeld) {
			// Another refresh is in flight and will warm every pool's cache
			// for everyone. This one's job is done.
			return nil
		}
		return fmt.Errorf("refresh lock: %w", err)
	}
	defer lock.Close() // close releases the flock

	cfg := config.Defaults()
	if cfgPath, err := config.Path(); err == nil {
		if loaded, err := config.Load(cfgPath); err == nil {
			cfg = loaded
		}
	}
	cfg = config.Validate(cfg, config.FieldSources{}, io.Discard)

	// attempted counts ACTIVE pools — the ones that actually ran. An enabled
	// pool with nothing configured inside it is skipped rather than counted
	// as a failure (R-35): it has no work to do, logging one line per
	// refresh forever would bury the real faults, and counting a skip as a
	// failure would hand a Following-only user exit 1 on every refresh. The
	// following skip is also load-bearing — feedstate.Update
	// garbage-collects every feed absent from its configured list, so
	// refreshing that pool with no feeds would erase the user's cadence
	// history and validators.
	attempted, failed := 0, 0
	if cfg.NewsActive() {
		attempted++
		if err := refreshNews(cfg, time.Now().UTC()); err != nil {
			failed++
			_ = refreshlog.Append(fmt.Sprintf("news: %s", err))
		}
	}
	if cfg.FollowingActive() {
		attempted++
		if err := refreshFollowing(context.Background(), cfg, time.Now().UTC()); err != nil {
			failed++
			_ = refreshlog.Append(fmt.Sprintf("following: %s", err))
		}
	}
	if attempted > 0 && failed == attempted {
		return fmt.Errorf("every active pool failed to refresh (%d of %d)", failed, attempted)
	}
	return nil
}

// refreshNews fetches the news pool's aggregators and rewrites feed.json.
// The fetch keeps the 5s budget it has always had — aggregators are single
// requests to fast JSON APIs, unlike the following pool's fan-out — and the
// write keeps the full-replace policy: a failed aggregator's prior stories
// drop out of the cache rather than ghosting indefinitely, self-healing on
// the next fully-successful refresh. (The following pool merges per feed
// instead; see mergeFollowingStories for why the two policies differ.)
//
// Per-source errors are logged under a "news <source>: " prefix. The prefix
// is not decoration: FetchAll keys its error map by source name while the
// following pool's is keyed by feed URL, so an unprefixed line cannot be
// attributed to a namespace by anyone reading refresh.log later.
//
// Every error this function returns is wrapped, for the same reason. The
// caller pastes it into refresh.log behind "news: ", where a bare "rename
// temp cache: ..." names neither the stage nor the file it failed on.
func refreshNews(cfg config.Config, now time.Time) error {
	path, err := cache.PoolPath("news")
	if err != nil {
		return fmt.Errorf("news cache path: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaults.FetchTimeout)
	defer cancel()
	stories, errs, err := multiFetch(ctx, cfg)
	if err != nil {
		return fmt.Errorf("fetch aggregators: %w", err)
	}
	for name, e := range errs {
		_ = refreshlog.Append(fmt.Sprintf("news %s: %s", name, e))
	}
	if len(stories) == 0 {
		// Nothing fetched: leave the stale cache in place rather than
		// blanking it, and report the pool as failed so the caller can tell
		// a total outage from a partial one.
		return errors.New("all aggregators returned no stories")
	}
	if err := writeCache(path, stories, now); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return nil
}

// multiFetch instantiates each Source named in cfg.News.Aggregators and
// runs them in parallel via fetch.FetchAll. Per-source errors flow back as
// a name→err map; the caller decides whether to log them, surface to the
// user, or both. A factory error (unknown source name) is treated as fatal
// because config.Validate is supposed to filter those out before we get
// here — if one slips through, that's a bug worth surfacing rather than
// silently degrading.
func multiFetch(ctx context.Context, cfg config.Config) ([]fetch.Story, map[string]error, error) {
	sources := make([]fetch.Source, 0, len(cfg.News.Aggregators))
	for _, name := range cfg.News.Aggregators {
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

// spawnRefresh launches the detached background refresh. Tests MAY swap
// this to observe or suppress the spawn, but MUST restore via t.Cleanup —
// same contract as newSource above.
//
// Sanctioned exception to the no-global-mutable-state convention
// (CLAUDE.md, ruled 2026-08-26): package-main test seams, swapped only in
// tests, restored via t.Cleanup.
var spawnRefresh = func() {
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
