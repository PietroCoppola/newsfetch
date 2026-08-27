package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"time"

	"golang.org/x/term"

	"github.com/PietroCoppola/newsfetch/internal/cache"
	"github.com/PietroCoppola/newsfetch/internal/config"
	"github.com/PietroCoppola/newsfetch/internal/defaults"
	"github.com/PietroCoppola/newsfetch/internal/fetch"
	"github.com/PietroCoppola/newsfetch/internal/render"
	"github.com/PietroCoppola/newsfetch/internal/session"
)

// maxStdinPayload bounds the statusline stdin read. The Claude Code
// payload is a few KB; the bound only guards against a runaway pipe.
const maxStdinPayload = 1 << 20

// errNoCachedStories reports that the cache had nothing to select from, so
// no story could be pinned. It travels out of the pin-creation closure to
// the caller, which answers it the way the unpinned path answers a cache
// miss: print nothing, spawn a refresh.
var errNoCachedStories = errors.New("statusline: no cached stories")

// errNoStorySelected reports that selection returned nothing from a
// non-empty pool. Nothing to pin, and nothing to render.
var errNoStorySelected = errors.New("statusline: selection produced no story")

// runStatusline renders the single-line statusline style. Design contract
// (docs/planning/statusline.md): never block on the network — a cache miss
// renders nothing and leaves a detached refresh behind; the next render
// picks up the warmed cache. Story selection is pinned to cli.pin (or the
// key extracted from the statusline stdin JSON) so re-renders within one
// user turn are stable; a new key selects fresh, records history, and pins.
//
// Selection and pinning happen inside one session.GetOrCreate, so the
// several renders a single user turn can fire concurrently agree on one
// story instead of each selecting its own and racing to persist it. If the
// store is unreachable — a wedged lock, an unresolvable path — the render
// degrades to an unpinned fresh selection: a wedged lock should cost
// stickiness, not the status row.
func runStatusline(out, errOut io.Writer, cfg config.Config, cli cliOverrides, rng *rand.Rand) error {
	pin := resolvePinKey(cli.pin, os.Stdin)
	width := cli.maxWidth
	if width <= 0 {
		width = defaults.TermWidth(defaults.BoxWidth)
	}
	// One clock reading for the whole invocation: the rendered age and any
	// pin written below then agree.
	now := time.Now().UTC()

	if pin != "" {
		if sPath, err := session.Path(); err == nil {
			// Set only when the entry is created here; a pin hit reads
			// no cache and so has no staleness to report.
			stale := false
			e, err := session.GetOrCreate(sPath, pin, func() (session.Entry, error) {
				return selectPinnedStory(cfg, errOut, rng, pin, now, &stale)
			})
			switch {
			case err == nil:
				fmt.Fprint(out, render.Statusline(fetch.Story{
					Title: e.Title, URL: e.URL, Author: e.Author, CreatedAt: e.CreatedAt,
				}, now, width))
				if stale {
					spawnRefresh()
				}
				return nil
			case errors.Is(err, errNoCachedStories):
				// Empty output beats a "no fresh news" line: in a status
				// row, silence is less noisy than an error banner.
				spawnRefresh()
				return nil
			default:
				fmt.Fprintln(errOut, "newsfetch: warning: session pin:", err)
				// Fall through: render unpinned rather than not at all.
			}
		}
	}

	path, err := cache.Path()
	if err != nil {
		return err
	}
	f, readErr := cache.Read(path)
	if readErr != nil || len(f.Stories) == 0 {
		spawnRefresh()
		return nil
	}

	seen := loadSeen(cfg, now, errOut)
	cfgOne := cfg
	cfgOne.Count = 1
	picked, err := selectFromPool(f.Stories, seen, cfgOne, now, rng)
	if err != nil {
		return err
	}
	if len(picked) == 0 {
		return nil
	}
	fmt.Fprint(out, render.Statusline(picked[0], now, width))
	recordHistory(picked, now, errOut)
	if !f.IsFresh(cfg.CacheTTL, now) {
		spawnRefresh()
	}
	return nil
}

// selectPinnedStory picks the story to pin for one user turn: read the
// cache, select against history, record what was selected. It runs under
// the session lock as session.GetOrCreate's create callback, so exactly one
// concurrent render reaches it per key. stale is set when the cache it read
// was past its TTL — the caller spawns the refresh once the lock is
// released rather than holding it across a process spawn.
//
// The entry carries the story's author and CreatedAt so later renders of
// the same pin reproduce this render's metadata tail exactly.
func selectPinnedStory(cfg config.Config, errOut io.Writer, rng *rand.Rand, pin string, now time.Time, stale *bool) (session.Entry, error) {
	path, err := cache.Path()
	if err != nil {
		return session.Entry{}, err
	}
	f, readErr := cache.Read(path)
	if readErr != nil || len(f.Stories) == 0 {
		return session.Entry{}, errNoCachedStories
	}
	seen := loadSeen(cfg, now, errOut)
	cfgOne := cfg
	cfgOne.Count = 1
	picked, err := selectFromPool(f.Stories, seen, cfgOne, now, rng)
	if err != nil {
		return session.Entry{}, err
	}
	if len(picked) == 0 {
		return session.Entry{}, errNoStorySelected
	}
	s := picked[0]
	recordHistory(picked, now, errOut)
	*stale = !f.IsFresh(cfg.CacheTTL, now)
	return session.Entry{
		Key: pin, Hash: s.Hash(), Title: s.Title, URL: s.URL,
		Author: s.Author, CreatedAt: s.CreatedAt, PinnedAt: now,
	}, nil
}

// resolvePinKey returns the story-pinning key: an explicit --pin wins;
// otherwise, when stdin is a pipe (the statusline invocation shape — the
// script feeds the session JSON in), the key is extracted from it. A TTY
// stdin means a human ran --style=statusline by hand: no key, fresh story
// per run.
func resolvePinKey(flagVal string, stdin *os.File) string {
	if flagVal != "" {
		return flagVal
	}
	if term.IsTerminal(int(stdin.Fd())) {
		return ""
	}
	return readPinKey(stdin)
}

// readPinKey extracts the pinning key from a Claude Code statusline JSON
// payload. prompt_id changes once per user message and is the chosen
// refresh granularity; session_id is the fallback when a payload predates
// prompt_id or omits it. Any read or parse failure returns "" — an
// unpinned render beats a failed one.
func readPinKey(r io.Reader) string {
	data, err := io.ReadAll(io.LimitReader(r, maxStdinPayload))
	if err != nil {
		return ""
	}
	var payload struct {
		PromptID  string `json:"prompt_id"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return ""
	}
	if payload.PromptID != "" {
		return payload.PromptID
	}
	return payload.SessionID
}
