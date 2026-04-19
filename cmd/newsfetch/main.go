// Command newsfetch renders one piece of bite-sized tech news each time a
// terminal opens. See spec.md at the repo root for the full design.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/cache"
	"github.com/PietroCoppola/newsfetch/internal/config"
	"github.com/PietroCoppola/newsfetch/internal/defaults"
	"github.com/PietroCoppola/newsfetch/internal/fetch"
	"github.com/PietroCoppola/newsfetch/internal/rank"
	"github.com/PietroCoppola/newsfetch/internal/refreshlog"
	"github.com/PietroCoppola/newsfetch/internal/render"
)

const refreshFlag = "--__refresh"

// newHNSource is the factory for the default HN source. Tests MAY swap
// this to return an httptest-backed source, but MUST restore via
// t.Cleanup(func() { newHNSource = original }) to avoid poisoning
// subsequent tests — failing tests would otherwise leak the swap.
var newHNSource = func() fetch.Source { return &fetch.HackerNews{} }

func main() {
	if len(os.Args) > 1 && os.Args[1] == refreshFlag {
		if err := runRefresh(); err != nil {
			_ = refreshlog.Append(err.Error())
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
	stories, err := hnFetch(ctx, cfg.MinPoints)
	if err != nil {
		fmt.Fprint(out, render.Fallback(defaults.FallbackMessage))
		return nil
	}
	story := rank.Select(stories, rank.Options{
		Topics:   cfg.Topics,
		Now:      now,
		PoolSize: defaults.RankPoolSize,
	}, rng)
	writeStory(out, story, cfg.Style, now)
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

Flags:
  --style=<mode>    display mode: boxed (default) | minimal | json
  --topics=<list>   comma-separated topics; explicit empty defeats config
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
	stories, err := hnFetch(ctx, cfg.MinPoints)
	if err != nil {
		return err
	}
	return writeCache(path, stories, time.Now().UTC())
}

func hnFetch(ctx context.Context, minPoints int) ([]fetch.Story, error) {
	src := newHNSource()
	stories, err := src.Fetch(ctx, fetch.FetchOptions{
		MinPoints: minPoints,
		Limit:     defaults.NumStories,
	})
	if err != nil {
		return nil, err
	}
	if len(stories) == 0 {
		// Zero-hits ≠ API error, but M2 folds both into the same
		// fallback. M8 polish may distinguish ("No stories matched"
		// vs "check your connection").
		return nil, errors.New("hn returned no stories")
	}
	return stories, nil
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
		fmt.Fprint(out, render.Boxed(s, now, defaults.BoxWidth))
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
