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
	"math"
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
	"github.com/PietroCoppola/newsfetch/internal/feedstate"
	"github.com/PietroCoppola/newsfetch/internal/fetch"
	"github.com/PietroCoppola/newsfetch/internal/history"
	"github.com/PietroCoppola/newsfetch/internal/lockfile"
	"github.com/PietroCoppola/newsfetch/internal/onboard"
	"github.com/PietroCoppola/newsfetch/internal/refreshlog"
	"github.com/PietroCoppola/newsfetch/internal/session"
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
		if err := runUninstall(os.Stdout, os.Stdin); err != nil {
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
// edit-existing path, --init is the bootstrap path), projects it through
// settingsAnswers, and routes the answer collection through the wizard or
// JSON-stdin depending on TTY status.
func runSettings(out io.Writer) error {
	return onboard.SettingsFlow(onboard.SettingsDeps{
		ConfigPath: config.Path,
		Current:    settingsCurrent,
		Answers:    pickSettingsAnswerSource(os.Stdin),
		Out:        out,
	})
}

// settingsCurrent is SettingsDeps.Current's production implementation: load
// the config file and project it into Answers. Pulled out of runSettings
// into a named function — not for testability alone, but because the
// closure it replaced carried a comment longer than the code — and that
// naming is what makes the decision below reachable from a test that
// drives the real call, rather than one that re-implements its two lines
// and quietly stops covering it the moment they diverge.
//
// Deliberately NOT config.Validate'd. Validate treats a feed with an
// unparseable, relative, or non-http(s) URL as unusable and drops it
// (splitFeeds in internal/config/validate.go), a rule that exists to keep
// bad feeds off the render/fetch path. But OverwriteConfig regenerates the
// ENTIRE file from Answers, so validating here would make the very next
// `--settings` save silently delete that feed's line from config.toml —
// the URL is the one part of a feed entry a user cannot get back, unlike
// an out-of-range max_items/weight, which is just a clamped number. A user
// who mistypes a URL currently sees a per-render warning and can fix the
// character; validating here would instead erase the line the first time
// they save settings for any unrelated reason. settingsAnswers's own guard
// is what keeps the OTHER end of this path safe — config.Load's internal
// "explicit zero" sentinel for a typed max_items=0/weight=0 — without
// paying that price. See TestSettingsAnswers_InvalidFeedURLSurvivesUnvalidated
// in main_test.go, which drives this exact function to pin the decision.
func settingsCurrent(path string) (onboard.Answers, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return onboard.Answers{}, err
	}
	return settingsAnswers(cfg), nil
}

// settingsAnswers projects a loaded Config into the Answers shape the
// --settings surfaces edit. It is a named function rather than an inline
// closure because it is the read half of a round trip that is lossy by
// default: onboard.OverwriteConfig regenerates the ENTIRE config file from
// Answers, so anything this projection drops is erased from the user's disk
// the first time they run --settings, silently and with no way to recover
// it. Being able to test it directly is worth the extra name.
//
// The MaxItems/Weight pointers are what carry "the user never set this"
// across the gap between the two representations: config uses a zero value
// for unset, Answers uses a nil pointer. Weight maps zero to nil and
// everything else to an address; MaxItems additionally substitutes
// config.Load's internal explicit-zero sentinel with an address of
// defaults.MinFeedItems rather than nil, since a genuinely absent key and a
// typed zero resolve to different values once reloaded (see the per-field
// comment below). An empty feed list maps to nil rather than an empty
// slice so the writer's nil-means-omit convention sees what it expects.
func settingsAnswers(cfg config.Config) onboard.Answers {
	var feeds []onboard.Feed
	for _, f := range cfg.Following.Feeds {
		of := onboard.Feed{URL: f.URL}
		// The rule: preserve what the user wrote when it can be
		// represented, substitute only when it cannot.
		//
		// config.Load hands back its internal -1 "explicit zero" sentinel
		// (config.explicitZeroMarker) for a max_items the user typed as 0
		// — Current deliberately does NOT run config.Validate (see its
		// comment: validating there would delete feeds with a bad URL, not
		// just clamp numbers), so this switch is the only thing standing
		// between that raw -1 and the rewritten file. A typed zero cannot
		// be represented (FeedConfig reserves 0 itself for "absent"), so it
		// is substituted with defaults.MinFeedItems — the value
		// config.Validate actually clamps an explicit zero to, and so the
		// value the program has been running that feed with all along.
		// Omitting the key instead, as an earlier round did, would read
		// back as genuinely unset and silently jump the effective cap to
		// defaults.DefaultFeedMaxItems (3) on the next --settings save. A
		// positive value, in range or not, CAN be represented, so it
		// round-trips exactly — an out-of-range value like 99 stays 99, and
		// the user keeps getting the warning that tells them to fix it.
		switch {
		case f.MaxItems > 0:
			n := f.MaxItems
			of.MaxItems = &n
		case f.MaxItems < 0:
			n := defaults.MinFeedItems
			of.MaxItems = &n
		}
		// Weight has no equivalent substitution: "unset" and what
		// config.Validate clamps an explicit 0 to (see clampFeedWeight)
		// are the same value, 0, so omitting the key here loses nothing.
		// NaN is deliberately left "set" (not caught by > 0, so carved
		// back in) so it still reaches tomlFloat's own non-finite guard
		// rather than being silently reclassified here.
		if f.Weight > 0 || math.IsNaN(f.Weight) {
			w := f.Weight
			of.Weight = &w
		}
		feeds = append(feeds, of)
	}
	return onboard.Answers{
		Topics:          cfg.Topics,
		Style:           cfg.Style,
		Pools:           cfg.Pools,
		PoolOrder:       cfg.PoolOrder,
		NewsAggregators: cfg.News.Aggregators,
		Count:           cfg.Count,
		FollowingCount:  cfg.FollowingCount,
		Feeds:           feeds,
		TickerMarker:    cfg.TickerMarker,
		TickerBoxed:     cfg.TickerBoxed,
		CacheTTLMinutes: int(cfg.CacheTTL / time.Minute),
		MinPoints:       cfg.MinPoints,
		DedupTTLHours:   int(cfg.DedupWindow / time.Hour),
	}
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

// runUninstall removes the shell rc block and offers to delete the files
// newsfetch created, in three groups: config, caches, and state. stdin's
// TTY status decides how much is on the table. A human is asked about all
// three. A piped run is offered config and caches only — the state group
// is withheld from the roster entirely and its location printed instead,
// so a script cannot silently destroy the user's dedup history and four
// weeks of feed cadence observation. That is not a new restriction: state
// has never been removable by any uninstall, so the piped path keeps
// doing exactly what it has always done and only the interactive path
// gains reach.
//
// in is a parameter rather than os.Stdin so tests can hand it a pipe.
func runUninstall(out io.Writer, in *os.File) error {
	interactive := term.IsTerminal(int(in.Fd()))
	deps := onboard.UninstallDeps{
		Shell:   onboard.Detect,
		Out:     out,
		Confirm: promptYesNo(in, out),
		Config:  onboard.Removable{Label: "config.toml", Path: config.Path},
		Caches:  cacheRemovables(),
	}
	if interactive {
		deps.State = stateRemovables()
	}
	if err := onboard.UninstallFlow(deps); err != nil {
		return err
	}
	if !interactive {
		printKeptState(out)
	}
	printKeptLocks(out)
	return nil
}

// cacheRemovables lists the data files newsfetch writes under the cache
// root. All of it is rebuildable by one fetch, which is why it sits in a
// group the piped path is allowed to clear.
//
// refresh.lock used to be listed here, on the grounds that a bare lock
// file left behind in an otherwise empty directory reads as litter. It is
// not litter: it is the entire mutual exclusion for the detached
// background refresh, which try-acquires it with a zero timeout and exits
// quietly when it is already held. Unlink it while a refresh is in flight
// and the next spawn creates a fresh inode at the same name, acquires
// that, and refreshes alongside the one already running — each believing
// it is the only one. A lock file's path is its identity, so no uninstall
// path removes one; printKeptLocks says so in a line.
func cacheRemovables() []onboard.Removable {
	return []onboard.Removable{
		{Label: "feed.json", Path: func() (string, error) { return cache.PoolPath("news") }},
		{Label: "following.json", Path: func() (string, error) { return cache.PoolPath("following") }},
		{Label: "refresh.log", Path: refreshlog.Path},
	}
}

// stateRemovables lists everything under the state root — the files whose
// loss costs the user something real: seen.json is the dedup memory,
// sessions.json the statusline pins, feeds.json up to four weeks of
// cadence observation that can only be re-earned in real time.
//
// Each one carries its flock sidecar as Lock rather than appearing as a
// fourth, fifth and sixth entry. All three are written under that lock by
// a concurrent newsfetch — the statusline takes sessions.lock on every
// terminal prompt — and unlinking a file out from under an flock holder
// does not stop it writing, so UninstallFlow takes the lock and holds it
// across the unlink. It never removes the sidecar: that path is the
// identity every other process coordinates on. The cache group needs no
// sidecar at all — everything in it is rebuildable by one fetch, which is
// why the piped path may clear it.
func stateRemovables() []onboard.Removable {
	return []onboard.Removable{
		{Label: "seen.json", Path: history.Path, Lock: lockSidecar(history.Path, "seen.lock")},
		{Label: "sessions.json", Path: session.Path, Lock: lockSidecar(session.Path, "sessions.lock")},
		{Label: "feeds.json", Path: feedstate.Path, Lock: lockSidecar(feedstate.Path, "feeds.lock")},
	}
}

// lockSidecar derives the path of the advisory-lock file that sits beside
// a state file, for Removable.Lock. The lock is an implementation detail
// of each package's read-modify-write, so history, session and feedstate
// deliberately export no accessor for it; widening three package APIs so
// uninstall could name three sidecars is the wrong trade. Resolver errors
// pass through unwrapped — UninstallFlow wraps them once, with the label
// attached.
func lockSidecar(base func() (string, error), name string) func() (string, error) {
	return func() (string, error) {
		p, err := base()
		if err != nil {
			return "", err
		}
		return filepath.Join(filepath.Dir(p), name), nil
	}
}

// printKeptState tells a piped --uninstall where the state it deliberately
// did not touch still lives. It names the directory and stops there. It
// deliberately does not print an rm -rf command: the path is interpolated
// from XDG_STATE_HOME, so a space or a shell metacharacter in it produces
// a command that means something other than what it reads as — and
// handing someone a destructive command they may paste without reading is
// a bad trade even when the quoting is right. Silent when no state file
// exists, so a machine that never rendered a story gets no confusing
// epilogue.
func printKeptState(out io.Writer) {
	seen, err := history.Path()
	if err != nil {
		return
	}
	dir := filepath.Dir(seen)
	kept := make([]string, 0, 3)
	for _, name := range []string{"seen.json", "sessions.json", "feeds.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			kept = append(kept, name)
		}
	}
	if len(kept) == 0 {
		return
	}
	fmt.Fprintf(out, "newsfetch: state kept in %s (%s); delete that directory to remove it\n",
		dir, strings.Join(kept, ", "))
}

// printKeptLocks names the .lock files --uninstall deliberately leaves on
// disk, in the same shape as the kept-state notice: the paths, one reason,
// and no command to paste. A lock file's path is its identity — unlink one
// and a second process can create and lock a fresh file at the same name
// while the first still holds the original — so uninstall removes the
// files a lock guards and never the lock. The files are empty, but a user
// who came to remove newsfetch is owed the fact that four zero-byte files
// survived it.
//
// The state sidecars are read off stateRemovables rather than named again
// here, so the two lists cannot drift. It prints on both the interactive
// and the piped path, and is silent when no lock file exists, so a machine
// that never ran newsfetch gets no epilogue.
func printKeptLocks(out io.Writer) {
	kept := make([]string, 0, 4)
	if dir, err := cache.Dir(); err == nil {
		kept = appendExisting(kept, refreshLockPath(dir))
	}
	for _, r := range stateRemovables() {
		if r.Lock == nil {
			continue
		}
		if p, err := r.Lock(); err == nil {
			kept = appendExisting(kept, p)
		}
	}
	if len(kept) == 0 {
		return
	}
	fmt.Fprintf(out, "newsfetch: lock files kept (%s); they are empty, and removing one while newsfetch is running would let two processes hold the same lock at once\n",
		strings.Join(kept, ", "))
}

// appendExisting appends path to dst if something is at path.
func appendExisting(dst []string, path string) []string {
	if _, err := os.Stat(path); err != nil {
		return dst
	}
	return append(dst, path)
}

// promptYesNo returns a Confirm function for UninstallFlow. If in is a
// TTY, the user is asked y/N once per group — config, caches, state. If in
// is not a TTY (script, pipe), the function returns true unconditionally:
// with no observer to ask, leaving files behind is worse than removing
// them, because the "left in place" line scrolls past unread and the user
// is left with orphans they never learn about.
//
// That unconditional yes used to be documented as reading --uninstall
// literally as "remove everything", and that phrasing is retired here
// rather than quietly contradicted. It was written when everything meant
// two rebuildable files, config.toml and feed.json. The roster has since
// grown to include seen.json and up to four weeks of cadence observation
// in feeds.json, and answering yes to those on a script's behalf is a
// different promise than the one that sentence made. What a piped run is
// allowed to remove is now decided one level up, in runUninstall, which
// withholds the state group from the roster entirely when stdin is not a
// TTY and prints where state was kept. This function still says yes to
// everything it is asked; it is simply never asked about state.
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
  --count=<n>       number of stories this render from the news pool: 1..4
                    (default 1; the following pool uses following_count,
                    config-only — M5 adds no new flags)
  --pin=<key>       statusline style: pin story selection to this key so
                    repeated renders stay stable; default reads prompt_id
                    (fallback session_id) from JSON on stdin
  --max-width=<n>   statusline style: truncate to n display columns
                    (default 80; detected terminal width when stdout is a TTY)

Subcommands:
  --init            interactive setup: pick topics, style, patch shell rc
                    if stdin is not a TTY, reads JSON instead:
                      {"topics": ["rust"], "style": "boxed"}
                      optional: pools, pool_order, count, following_count,
                      ticker_marker, ticker_boxed,
                      news: {aggregators: [...]},
                      following: {feeds: [{url, max_items, weight}]}
  --settings        edit existing config: topics, style, pools, feeds,
                    counts, ticker
                    if stdin is not a TTY, reads JSON instead:
                      {"topics": ["rust"], "style": "boxed",
                       "pools": ["news"], "count": 1,
                       "news": {"aggregators": ["hackernews"]}}
                      topics, style, pools, count required; everything
                      else optional and preserved from current config
  --uninstall       remove shell rc block, config, and caches (see README);
                    interactive: asks per group; piped: no prompt, keeps
                    dedup/session/feed state and prints where

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
// refreshLockPath names the sidecar that makes runRefresh single-flight,
// in the cache directory dir. It is a function so the refresh and the
// uninstall notice that reports the lock as kept cannot disagree about
// where it lives.
func refreshLockPath(dir string) string {
	return filepath.Join(dir, "refresh.lock")
}

func runRefresh() error {
	dir, err := cache.Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	lock, err := lockfile.Acquire(refreshLockPath(dir), 0)
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
