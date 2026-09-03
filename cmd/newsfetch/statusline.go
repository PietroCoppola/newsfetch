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

// errNoCachedStories reports that no ACTIVE pool had cached stories, so no
// story could be pinned. Active, not enabled (R-35): a pool the config has
// switched on but left with nothing to fetch from is not read at all. It travels out of the pin-creation closure to the
// caller, which answers it the way the unpinned path answers a cache miss:
// print nothing, spawn a refresh.
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
//
// Both paths select through pickStatusline and differ only in what they do
// with the story it returns: render it, or build the session.Entry that
// later renders of the same pin will reproduce. Keeping that in one place is
// what stops a pinned turn and an unpinned turn from picking differently.
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
			// no cache and so has no staleness to report. The spawn it
			// gates happens below, after GetOrCreate has released
			// sessions.lock — a detached process must never be started
			// from inside the critical section.
			stale := false
			e, err := session.GetOrCreate(sPath, pin, func() (session.Entry, error) {
				// loadSeen stays inside the callback: a pin hit never
				// runs it, and so never reads seen.json at all.
				seen := loadSeen(cfg, now, errOut)
				s, needRefresh, err := pickStatusline(cfg, seen, now, rng, errOut)
				// Captured before the error check: errNoCachedStories carries
				// a real needRefresh value too (a present, fresh, empty pool
				// answers false; a missing or stale one answers true), and the
				// caller below must see it on that path, not just on success.
				stale = needRefresh
				if err != nil {
					return session.Entry{}, err
				}
				// The entry carries the story's author and CreatedAt so
				// later renders of the same pin reproduce this render's
				// metadata tail exactly.
				return session.Entry{
					Key: pin, Hash: s.Hash(), Title: s.Title, URL: s.URL,
					Author: s.Author, CreatedAt: s.CreatedAt, PinnedAt: now,
				}, nil
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
				// row, silence is less noisy than an error banner. The
				// spawn itself is gated on stale, not unconditional: a
				// pool that is present, fresh, and simply empty must not
				// fork a refresh on every prompt, forever (R-36).
				if stale {
					spawnRefresh()
				}
				return nil
			default:
				fmt.Fprintln(errOut, "newsfetch: warning: session pin:", err)
				// Fall through: render unpinned rather than not at all.
			}
		}
	}

	seen := loadSeen(cfg, now, errOut)
	s, needRefresh, err := pickStatusline(cfg, seen, now, rng, errOut)
	switch {
	case err == nil:
		fmt.Fprint(out, render.Statusline(s, now, width))
		if needRefresh {
			spawnRefresh()
		}
		return nil
	case errors.Is(err, errNoCachedStories):
		// Empty output beats a "no fresh news" line, exactly as on the
		// pinned path above. Gated on needRefresh, not unconditional: a
		// pool that is present, fresh, and simply empty must not fork a
		// refresh on every prompt, forever (R-36).
		if needRefresh {
			spawnRefresh()
		}
		return nil
	case errors.Is(err, errNoStorySelected):
		// A non-empty cache that yielded nothing is not worth a warning:
		// there is simply no line to draw this turn.
		return nil
	default:
		return err
	}
}

// statuslineCache is one pool's cache as the statusline needs to see it: the
// stories it holds, whether the file was there and readable at all, and
// whether it is inside the TTL. A pool that is inactive is never read and
// keeps the zero value, which is never consulted for staleness either.
type statuslineCache struct {
	stories []fetch.Story
	present bool
	fresh   bool
}

// needsRefresh reports whether this pool would benefit from a background
// refresh: its cache is absent, unreadable, or past its TTL. A missing cache
// and an expired one are the same answer, yes.
//
// Emptiness is deliberately no part of it (R-36). A pool whose feeds
// legitimately refreshed to zero stories is present and fresh: it offers
// nothing to render and asks for nothing. Reading "no stories" as "no cache"
// here would spawn a detached process on every prompt of every turn, forever,
// for anyone whose feeds have gone quiet — of every surface in this binary,
// this is the worst one to get that wrong.
func (c statuslineCache) needsRefresh() bool {
	return !c.present || !c.fresh
}

// readStatuslinePool reads one pool's cache file through readPoolCache — the
// same helper the boxed render path uses, in this same package
// (cmd/newsfetch/pools.go). One staleness rule, one implementation: two
// surfaces disagreeing about what "stale" means would have one of them
// spawning refreshes the other thought unnecessary.
//
// A PoolPath error folds into the zero value for the same reason a missing
// file does: a status row has no room to report a cache fault, and the
// caller's response to both is identical — try the next pool, and leave a
// refresh behind.
func readStatuslinePool(pool string, ttl time.Duration, now time.Time) statuslineCache {
	path, err := cache.PoolPath(pool)
	if err != nil {
		return statuslineCache{}
	}
	stories, present, fresh := readPoolCache(path, ttl, now)
	return statuslineCache{stories: stories, present: present, fresh: fresh}
}

// pickOnePool selects a single story from one pool and records it as
// rendered. The bool is false when the pool had nothing left to give: with
// the all-seen bypass off that is precisely the "fully seen" signal
// precedence needs, and it is why the bypass is a parameter rather than a
// constant.
func pickOnePool(p poolPick, seen map[string]struct{}, cfg config.Config, bypassWhenAllSeen bool, now time.Time, rng *rand.Rand, errOut io.Writer) (fetch.Story, bool, error) {
	picked, err := selectFromPool(p, seen, cfg, bypassWhenAllSeen, now, rng)
	if err != nil {
		return fetch.Story{}, false, err
	}
	if len(picked) == 0 {
		return fetch.Story{}, false, nil
	}
	recordHistory(picked, now, errOut)
	return picked[0], true, nil
}

// pickStatusline chooses the one story the status row will show and records
// it as rendered. It is the single selection path both statusline callers
// use — the unpinned render and the pinned create-callback — so a pinned turn
// and an unpinned turn can never pick from different pools.
//
// Pools are chosen by PRECEDENCE, never by competition (design addendum item
// 1): scores are never compared across pools, and a stale-but-present
// following cache beats a fresh news one, because freshness does not reorder
// precedence. Top-down:
//
//   - following, filtered against the shared seen set with the all-seen
//     bypass OFF. That bypass is what makes a pool unable to report "fully
//     seen"; leaving it on here would pin the status row to a repeated
//     followed story forever and make news unreachable.
//   - news, with the bypass OFF as well and the count forced to 1. Ruling
//     R-31 is one rule serving both the boxed path and this one, and it
//     spends the bypass exactly once per invocation, in the last-resort pass
//     at the bottom — never in this first pass. So both pools are treated
//     identically here.
//
// Each pool is reached only when it is ACTIVE (R-35) — enabled and holding
// something to fetch from — which is the same gate runRefresh skips on.
//
// The v0.6.0 guarantee for a user with no feeds is preserved by that
// last-resort pass rather than by this branch: their single fully-seen news
// pool comes back empty from the first pass and is re-shown by the second,
// so the status row repeats instead of going blank.
//
// The second return value reports that some active pool wants a refresh. It
// is a return value rather than a spawn because this function runs under
// sessions.lock on the pinned path: the caller starts the one detached
// refresh — which warms every active pool — after the lock is released.
//
// The pick is always exactly one story: the statusline is a single line, so
// neither count nor following_count means anything here.
//
// This function must never reach the network. fetch.Following.FetchFeeds is
// deliberately unreachable from this file: a cold following cache falls
// through to news, it does not fetch.
func pickStatusline(cfg config.Config, seen map[string]struct{}, now time.Time, rng *rand.Rand, errOut io.Writer) (fetch.Story, bool, error) {
	var following, news statuslineCache
	var weights map[string]float64
	needRefresh := false

	// Activity, not enablement (R-35). An inactive pool is not read, not
	// ranked, and not counted stale: a news pool whose aggregator list the
	// user emptied has nothing to refresh from, so reading it would serve
	// ghost stories out of the feed.json written before that edit while
	// asking, on every prompt, for a refresh that deliberately skips it.
	// These are the same two gates runRefresh uses.
	if cfg.FollowingActive() {
		following = readStatuslinePool("following", cfg.CacheTTL, now)
		needRefresh = needRefresh || following.needsRefresh()
		// A feed the user unsubscribed from stops showing here on the next
		// render, not on the next successful refresh — the same rule
		// assemblePools applies to the boxed path, and applied on both
		// surfaces because both read following.json for themselves. Note
		// the order: staleness is read off the FILE first (R-36), so a
		// present, fresh cache that this filter empties still asks for
		// nothing.
		following.stories = configuredFeedStories(following.stories, cfg.FeedURLs())
	}
	if cfg.NewsActive() {
		news = readStatuslinePool("news", cfg.CacheTTL, now)
		needRefresh = needRefresh || news.needsRefresh()
	}
	if len(following.stories) == 0 && len(news.stories) == 0 {
		// No active pool has anything to select from, so there is no
		// line to draw; the caller prints nothing and spawns a refresh,
		// because a blank status row is worth retrying for.
		//
		// This is emptiness deciding what there is to SELECT FROM, which
		// is a different question from needRefresh above: a present,
		// fresh, empty pool passes through here without ever having
		// asked for a refresh (R-36).
		return fetch.Story{}, needRefresh, errNoCachedStories
	}

	// Both branches below test the story count rather than presence: a
	// present-but-empty pool has nothing to rank, and skipping it keeps the
	// feeds.json read under it off the hot path.
	if len(following.stories) > 0 {
		// feeds.json is read only here, once the following pool is
		// active AND has stories to rank, so a user with no feeds — or
		// with a quiet, empty following cache — pays nothing on a path
		// that runs on every prompt. Local file read only: the
		// never-blocks-on-the-network contract holds.
		weights = feedWeights(cfg, now)
		s, ok, err := pickOnePool(poolPick{
			Name:    "following",
			Stories: following.stories,
			Count:   1,
			Weights: weights,
		}, seen, cfg, false, now, rng, errOut)
		if err != nil {
			return fetch.Story{}, needRefresh, err
		}
		if ok {
			return s, needRefresh, nil
		}
	}

	if len(news.stories) > 0 {
		// Label is deliberately empty here and above: labels are box
		// chrome, and nothing in a status row is ever labelled. Bypass
		// OFF, exactly as for following: R-31 spends it only below.
		s, ok, err := pickOnePool(poolPick{
			Name:    "news",
			Stories: news.stories,
			Count:   1,
		}, seen, cfg, false, now, rng, errOut)
		if err != nil {
			return fetch.Story{}, needRefresh, err
		}
		if ok {
			return s, needRefresh, nil
		}
	}

	// Ruling R-31: repeats beat silence, but only as a last resort. Reaching
	// here means every pool was selected with the all-seen bypass OFF and
	// came back empty, while at least one of them held cached stories — so
	// every present pool is fully seen. Run them again in pool_order with
	// the bypass ON and re-show a seen story rather than render a blank
	// status row; the first pool to yield wins, exactly as in assemblePools.
	for _, name := range cfg.PoolOrder {
		var p poolPick
		switch {
		case name == "following" && len(following.stories) > 0:
			p = poolPick{Name: "following", Stories: following.stories, Count: 1, Weights: weights}
		case name == "news" && len(news.stories) > 0:
			p = poolPick{Name: "news", Stories: news.stories, Count: 1}
		default:
			// Inactive, so never read; or its cache was missing,
			// unreadable, or empty. Nothing to re-show either way,
			// and re-testing what was actually read (rather than
			// what the config enables) is what keeps an inactive
			// news pool's stale feed.json out of the status row.
			continue
		}
		s, ok, err := pickOnePool(p, seen, cfg, true, now, rng, errOut)
		if err != nil {
			return fetch.Story{}, needRefresh, err
		}
		if ok {
			return s, needRefresh, nil
		}
	}

	return fetch.Story{}, needRefresh, errNoStorySelected
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
