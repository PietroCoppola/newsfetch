package main

import (
	"encoding/json"
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

// runStatusline renders the single-line statusline style. Design contract
// (docs/planning/statusline.md): never block on the network — a cache miss
// renders nothing and leaves a detached refresh behind; the next render
// picks up the warmed cache. Story selection is pinned to cli.pin (or the
// key extracted from the statusline stdin JSON) so re-renders within one
// user turn are stable; a new key selects fresh, records history, and pins.
func runStatusline(out, errOut io.Writer, cfg config.Config, cli cliOverrides, rng *rand.Rand) error {
	pin := resolvePinKey(cli.pin, os.Stdin)
	width := cli.maxWidth
	if width <= 0 {
		width = defaults.TermWidth(defaults.BoxWidth)
	}

	// Pin hit: render the stored story and stop. No cache read, no history
	// write, no refresh check — this is the every-assistant-message path
	// and stays as close to a single file read as possible.
	if pin != "" {
		if sPath, err := session.Path(); err == nil {
			if f, err := session.Read(sPath); err == nil {
				if e, ok := f.Lookup(pin); ok {
					fmt.Fprint(out, render.Statusline(fetch.Story{Title: e.Title, URL: e.URL}, width))
					return nil
				}
			}
		}
	}

	path, err := cache.Path()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	f, readErr := cache.Read(path)
	if readErr != nil || len(f.Stories) == 0 {
		// Empty output beats a "no fresh news" line: in a status row,
		// silence is less noisy than an error banner.
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
	s := picked[0]
	fmt.Fprint(out, render.Statusline(s, width))
	recordHistory(picked, now, errOut)
	if pin != "" {
		if sPath, err := session.Path(); err == nil {
			if pinErr := session.Pin(sPath, session.Entry{
				Key: pin, Hash: s.Hash(), Title: s.Title, URL: s.URL, PinnedAt: now,
			}); pinErr != nil {
				fmt.Fprintln(errOut, "newsfetch: warning: session pin:", pinErr)
			}
		}
	}
	if !f.IsFresh(cfg.CacheTTL, now) {
		spawnRefresh()
	}
	return nil
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
