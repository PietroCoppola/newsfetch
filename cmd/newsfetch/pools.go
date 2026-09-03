package main

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/cache"
	"github.com/PietroCoppola/newsfetch/internal/config"
	"github.com/PietroCoppola/newsfetch/internal/defaults"
	"github.com/PietroCoppola/newsfetch/internal/feedstate"
	"github.com/PietroCoppola/newsfetch/internal/fetch"
	"github.com/PietroCoppola/newsfetch/internal/rank"
	"github.com/PietroCoppola/newsfetch/internal/refreshlog"
	"github.com/PietroCoppola/newsfetch/internal/render"
)

// poolPick is one pool's raw material for selection: what it fetched or
// had cached, how many stories it is allowed to contribute, and the
// per-feed cadence weights that apply inside it. Weights is nil for
// aggregator pools — pools never rank against each other, so a weight
// map from one pool must never reach another's SelectN call.
type poolPick struct {
	Name    string
	Label   string
	Stories []fetch.Story
	Count   int
	Weights map[string]float64 // nil for aggregator pools
}

// selectFromPool pre-filters p.Stories against seen, then picks p.Count
// stories with diversity-aware multi-selection.
//
// bypassWhenAllSeen is the caller's call, not this function's: with it
// off, a pool whose every story has already been rendered contributes
// nothing, which is what lets a second pool fill the render instead. With
// it on, the filter is dropped and the pool re-shows something it has
// shown before. Ruling R-31 spends that bypass exactly once per
// invocation, as a last resort — repeats beat silence, but only after
// every pool has been given a chance to offer something new.
//
// An empty pool is not an error: a cold cache is the normal
// first-run-of-a-new-pool case, and rank.SelectN would reject it.
//
// The within-pool collapse is the other half of R-38. The working set
// assemblePools threads between pools stops one article being printed by
// two DIFFERENT pools, and rank.Filter drops what was already shown, but
// neither looks at two candidates inside THIS pool that are the same
// article — an aggregator feed sitting beside the blog it mirrors is the
// ordinary way that happens — so at a count above 1 the pool could fill
// two of its own slots with one story.
func selectFromPool(p poolPick, seen map[string]struct{}, cfg config.Config, bypassWhenAllSeen bool, now time.Time, rng *rand.Rand) ([]fetch.Story, error) {
	if len(p.Stories) == 0 {
		return nil, nil
	}
	candidates := rank.Filter(p.Stories, seen)
	if len(candidates) == 0 {
		if !bypassWhenAllSeen {
			return nil, nil
		}
		candidates = p.Stories
	}
	// After the bypass, so the last-resort path collapses too: repeating
	// one article is the concession R-31 makes, printing it twice in the
	// same box is not.
	candidates = dedupByHash(candidates)
	picked, err := rank.SelectN(candidates, p.Count, rank.Options{
		Topics:      cfg.Topics,
		Now:         now,
		PoolSize:    defaults.RankPoolSize,
		FeedWeights: p.Weights,
	}, rng)
	if err != nil {
		return nil, fmt.Errorf("select stories: %w", err)
	}
	return picked, nil
}

// dedupByHash keeps the first candidate for each fetch.Story.Hash,
// preserving order. Hash normalises away tracking parameters, host casing
// and www./m. prefixes, which is exactly what makes the same article
// arriving from two feeds detectable as one story.
//
// First-seen wins rather than best-scoring: the surviving copies are ranked
// afterwards anyway, and picking a winner here would put a second, quieter
// ranking rule in a function whose whole job is to count articles.
func dedupByHash(stories []fetch.Story) []fetch.Story {
	seen := make(map[string]struct{}, len(stories))
	out := stories[:0:0]
	for _, s := range stories {
		h := s.Hash()
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, s)
	}
	return out
}

// feedWeights builds the following pool's per-feed cadence multipliers,
// keyed on fetch.Story.Feed the way rank.Options.FeedWeights expects.
//
// Gated on FollowingActive (R-25): a user with no feeds must not pay a
// state-file read on the hot path, and the weights are recomputed at
// render time rather than snapshotted into the cache because the 4-week
// window keeps moving even when the cached document does not.
//
// Manual per-feed weights REPLACE the cadence weight rather than
// multiplying it — the design's manual override "bypasses the
// rolling-average computation" outright, which is the whole point of
// setting one: a user pinning an archived blog to 0.3 wants 0.3, not 0.3
// times whatever the cadence maths happened to produce this week.
//
// A missing or corrupt feeds.json degrades to manual weights only. Losing
// the cadence boost is a ranking nuance; failing the render is not.
func feedWeights(cfg config.Config, now time.Time) map[string]float64 {
	if !cfg.FollowingActive() {
		return nil
	}
	weights := map[string]float64{}
	if path, err := feedstate.Path(); err == nil {
		if f, err := feedstate.Read(path); err == nil {
			weights = f.Weights(cfg.FeedURLs(), now)
		}
	}
	for _, feed := range cfg.Following.Feeds {
		if feed.Weight > 0 {
			weights[feed.URL] = feed.Weight
		}
	}
	return weights
}

// fallbackMessage returns the user-facing string for the render that has
// nothing to show. With exactly one aggregator configured AND the
// following pool inactive, name the provider so the user knows which one
// to investigate; anything else stays generic, because blaming one of
// several configured sources — or blaming an aggregator when the user's
// feeds are the empty half — would be wrong (R-21).
//
// Configured-count is the signal rather than which source actually
// failed: it is stable per invocation and needs no peek at the error map
// from the render site. Partial-failure messaging is an M8 lever.
func fallbackMessage(cfg config.Config) string {
	if len(cfg.News.Aggregators) == 1 && !cfg.FollowingActive() {
		return fmt.Sprintf("%s unavailable — check your connection", cfg.News.Aggregators[0])
	}
	return defaults.FallbackMessage
}

// assemblePools reads every ACTIVE pool's cache, selects from each, and
// returns the render.Pool slice in cfg.PoolOrder sequence plus the
// concatenation of every picked story in render order (hero first per
// pool, pools in pool_order) for recordHistory — so seen.json keeps
// reflecting exactly what was printed.
//
// Selection is two-pass, which is ruling R-31 and the answer to the last
// open question in the plan (a fully-seen following pool alongside an
// unusable news pool): pass one selects every pool with the all-seen
// bypass OFF, so a pool with nothing new steps aside and lets another
// pool fill the render. Only if that leaves EVERY pool empty while at
// least one pool had cached stories does pass two run, in pool_order,
// with the bypass ON — and the first pool to yield content wins, the
// rest staying empty. Repeats beat silence, but only as a last resort,
// and only one pool pays for it.
//
// Pool-specific behaviour on a cold cache is deliberately asymmetric.
// The news pool keeps the pre-pool cold path: one synchronous fetch under
// defaults.FetchTimeout, full-replace cache write. The following pool
// does NOT fan out here (R-24) — its per-feed sub-timeouts would be
// clipped by the render parent and N feed requests do not belong on the
// startup path — so a cold following.json renders nothing and waits for
// the detached refresh this function spawns.
//
// Dedup spans pools (R-38). Each pool selects against a working seen set
// that starts as a COPY of the caller's history set and gains every pick
// before the next pool runs, so an article carried by both Hacker News and
// a followed feed — fetch.Story.Hash normalises URLs precisely so those are
// one story — lands in one box, the earlier one in pool_order.
//
// Exactly one refresh is spawned per invocation, when ANY active pool's
// cache is missing, unreadable or past cfg.CacheTTL. Emptiness is NOT
// staleness (R-36): a pool that legitimately refreshed to zero stories
// reads as present and fresh, renders nothing, and asks for nothing. The
// spawn lives here rather than in the caller because this is where cache
// freshness is known, and one detached process refreshes every pool.
func assemblePools(cfg config.Config, seen map[string]struct{}, now time.Time, rng *rand.Rand, errOut io.Writer) ([]render.Pool, []fetch.Story, error) {
	picks := make([]poolPick, 0, len(cfg.PoolOrder))
	needRefresh := false
	for _, name := range cfg.PoolOrder {
		// Activity, not enablement (R-35). NewsActive and FollowingActive
		// are this plan's single definition of "the pool is worth
		// reading", and they are the same gates runRefresh skips on — so a
		// user who empties [news] aggregators cannot keep seeing ghost
		// stories out of a feed.json written before that edit while
		// requesting a refresh that deliberately does nothing. An
		// unrecognised name is inert; config.Validate has already dropped
		// it, and PoolPath would only error on it here.
		switch name {
		case "news":
			if !cfg.NewsActive() {
				continue
			}
		case "following":
			if !cfg.FollowingActive() {
				continue
			}
		default:
			continue
		}
		path, err := cache.PoolPath(name)
		if err != nil {
			return nil, nil, fmt.Errorf("pool %q: %w", name, err)
		}
		stories, present, fresh := readPoolCache(path, cfg.CacheTTL, now)
		if name == "following" {
			// The configured feed list decides what may render, not what
			// happens to be on disk. Filtering AFTER readPoolCache, never
			// inside it: presence and freshness describe the FILE (R-36),
			// and must not move because the user edited their feed list.
			stories = configuredFeedStories(stories, cfg.FeedURLs())
		}
		if name == "news" && len(stories) == 0 {
			fetched, err := fetchNewsCold(cfg, path, errOut)
			if err != nil {
				return nil, nil, err
			}
			if len(fetched) > 0 {
				stories, present, fresh = fetched, true, true
			}
		}
		if !present || !fresh {
			needRefresh = true
		}
		p := poolPick{
			Name:    name,
			Label:   defaults.PoolLabel(name),
			Stories: stories,
			Count:   cfg.Count,
		}
		if name == "following" {
			p.Count = cfg.FollowingCount
			// Only pay the feedstate read when there is something to rank
			// with it; a cold following pool skips the hot-path cost.
			if len(stories) > 0 {
				p.Weights = feedWeights(cfg, now)
			}
		}
		picks = append(picks, p)
	}

	// R-38: one article, one box. fetch.Story.Hash normalises URLs so the
	// same article from Hacker News and from a followed feed is ONE story;
	// selecting every pool against the same untouched history set would
	// print it twice in a single invocation. The working set starts as a
	// COPY of the caller's map — that map is history, not scratch space,
	// and recordHistory has not been told about this render yet — and
	// grows with each pool's picks.
	working := make(map[string]struct{}, len(seen))
	for h := range seen {
		working[h] = struct{}{}
	}

	picked := make([][]fetch.Story, len(picks))
	anyCached, anyContent := false, false
	for i, p := range picks {
		if len(p.Stories) > 0 {
			anyCached = true
		}
		sel, err := selectFromPool(p, working, cfg, false, now, rng)
		if err != nil {
			return nil, nil, err
		}
		picked[i] = sel
		// picks is in pool_order, so the earlier pool keeps a contested
		// article and later pools select around it — the same precedence
		// the statusline and the two-pass bypass already use.
		for _, s := range sel {
			working[s.Hash()] = struct{}{}
		}
		if len(sel) > 0 {
			anyContent = true
		}
	}
	// Pass two needs no working set of its own: it runs only when pass one
	// selected nothing anywhere, so working is still exactly the caller's
	// set, and it stops at the first pool that yields.
	if !anyContent && anyCached {
		for i, p := range picks {
			sel, err := selectFromPool(p, working, cfg, true, now, rng)
			if err != nil {
				return nil, nil, err
			}
			if len(sel) > 0 {
				picked[i] = sel
				break
			}
		}
	}

	pools := make([]render.Pool, 0, len(picks))
	var rendered []fetch.Story
	for i, p := range picks {
		pools = append(pools, render.Pool{Name: p.Name, Label: p.Label, Stories: picked[i]})
		rendered = append(rendered, picked[i]...)
	}
	if needRefresh {
		spawnRefresh()
	}
	return pools, rendered, nil
}

// configuredFeedStories keeps only the following-pool stories attributed to
// a currently configured feed. Story.Feed is the attribution key, the same
// one mergeFollowingStories decides on.
//
// It exists because pruning on the write side alone is not enough. The merge
// that drops an unsubscribed feed runs only when a refresh brought at least
// one feed result back, so between the unsubscribe and the next at least
// partly successful refresh the cache still holds the removed feed's
// stories — and a feed set where every fetch keeps failing makes that
// interval unbounded. Both read surfaces filter, because both read
// following.json independently: filtering on one of them would leave the
// other showing a feed the user removed.
//
// An unattributed story (empty Feed) cannot be matched against the list and
// so does not survive. Nothing writes one — fetch.Following stamps every
// story with the configured URL it came from — and for a torn or
// hand-edited file, dropping what cannot be attributed is the direction
// that honours the user's list.
func configuredFeedStories(stories []fetch.Story, configured []string) []fetch.Story {
	if len(stories) == 0 {
		return stories
	}
	allowed := make(map[string]bool, len(configured))
	for _, url := range configured {
		allowed[url] = true
	}
	out := make([]fetch.Story, 0, len(stories))
	for _, s := range stories {
		if allowed[s.Feed] {
			out = append(out, s)
		}
	}
	return out
}

// readPoolCache reads one pool's cache file, reporting its stories, whether
// the file was there and readable at all, and whether it is inside ttl.
//
// Presence and freshness are independent of emptiness (R-36). A file that
// read cleanly is present, and fresh-or-not on its FetchedAt alone, even
// when it holds zero stories: a following pool whose feeds legitimately
// refreshed to nothing is not stale, and must not be made to demand a
// detached refresh on every terminal open and every statusline prompt for
// the rest of time. A missing or torn file is neither present nor fresh,
// which is exactly the state a refresh repairs.
//
// This says nothing about rendering. An empty pool still renders nothing —
// that rule lives in the empty-pool handling downstream, and reads the
// story count, not these flags.
func readPoolCache(path string, ttl time.Duration, now time.Time) (stories []fetch.Story, present, fresh bool) {
	f, err := cache.Read(path)
	if err != nil {
		return nil, false, false
	}
	return f.Stories, true, f.IsFresh(ttl, now)
}

// fetchNewsCold is the news pool's cold-start path, carried over from the
// pre-pool render path unchanged: one synchronous multi-source fetch
// under defaults.FetchTimeout, per-source failures to refresh.log, and a
// full-replace cache write.
//
// Full-replace on partial fetch: a failed source's prior stories drop out
// of the cache rather than ghosting indefinitely, self-healing on the
// next fully-successful refresh. A cache write failure is a warning, not
// a render failure — the user still gets their story.
func fetchNewsCold(cfg config.Config, path string, errOut io.Writer) ([]fetch.Story, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaults.FetchTimeout)
	defer cancel()
	stories, errs, err := multiFetch(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("news pool cold-start fetch: %w", err)
	}
	for name, e := range errs {
		_ = refreshlog.Append(fmt.Sprintf("%s: %s", name, e))
	}
	if len(stories) == 0 {
		return nil, nil
	}
	if writeErr := writeCache(path, stories, time.Now().UTC()); writeErr != nil {
		fmt.Fprintln(errOut, "newsfetch: warning: could not write cache:", writeErr)
	}
	return stories, nil
}

// writePools dispatches the assembled pools to the renderer named by
// cfg.Style. The caller has already validated cfg; an unknown style falls
// back to boxed (belt-and-suspenders).
//
//   - minimal: pools stacked, one blank line between them, no labels.
//   - json:    a uniform top-level array with a pool field on every
//     object, unconditionally (R-3).
//   - boxed:   one render width, computed ONCE and shared by every pool
//     so the stacked boxes align. Two or more rendering pools get header
//     labels; one pool gets none, which keeps the common case
//     byte-identical to the pre-pool render.
//
// The nothing-to-show render is dispatched here rather than short-circuited
// by the caller, because it is style-dependent too. boxed and minimal print
// the fallback sentence, unchanged. json does NOT: R-3's uniform top-level
// array has no exception for an empty render, and an empty array is both
// the honest answer and a parseable one. Printing prose down a --style=json
// pipe broke `newsfetch --style=json | jq` on a healthy install the moment
// a pool read as present-but-empty, which R-36 made an ordinary state
// rather than a network failure.
func writePools(out io.Writer, pools []render.Pool, cfg config.Config, now time.Time) error {
	if cfg.Style != "json" && !anyStories(pools) {
		fmt.Fprint(out, render.Fallback(fallbackMessage(cfg)))
		return nil
	}
	switch cfg.Style {
	case "minimal":
		fmt.Fprint(out, render.MinimalPools(pools, now))
	case "json":
		fmt.Fprint(out, render.JSONPools(pools, now))
	default:
		rendered, err := render.Pools(pools, now, defaults.TermWidth(defaults.BoxWidth), render.MultiOptions{
			Marker: render.TickerMarker(cfg.TickerMarker),
			Boxed:  cfg.TickerBoxed,
		})
		if err != nil {
			return fmt.Errorf("render: %w", err)
		}
		fmt.Fprint(out, rendered)
	}
	return nil
}

// anyStories reports whether any pool has something to render. It reads
// the story count rather than the pool count on purpose: a pool that read
// cleanly and held nothing is present and fresh (R-36) but still renders
// nothing, so pools carrying only empty pools is the ordinary
// nothing-to-show shape, not an error.
func anyStories(pools []render.Pool) bool {
	for _, p := range pools {
		if len(p.Stories) > 0 {
			return true
		}
	}
	return false
}
