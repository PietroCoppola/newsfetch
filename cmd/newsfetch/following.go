package main

import (
	"context"
	"fmt"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/cache"
	"github.com/PietroCoppola/newsfetch/internal/config"
	"github.com/PietroCoppola/newsfetch/internal/defaults"
	"github.com/PietroCoppola/newsfetch/internal/feedstate"
	"github.com/PietroCoppola/newsfetch/internal/fetch"
	"github.com/PietroCoppola/newsfetch/internal/refreshlog"
)

// observations converts one FetchFeeds pass into the records feedstate
// persists: publish dates and document shape for the cadence weighting,
// validators for the next conditional GET.
//
// Only successful feeds appear here, and that is deliberate. FetchFeeds
// reports failures in a separate map keyed by feed URL and returns no
// FeedResult for them, so a feed that timed out, 500'd or served
// unparseable XML simply keeps the state it already had. Synthesising a
// zero-valued observation for it would be actively wrong twice over: it
// would tell feedstate the document went empty — which, for a feed that
// has published before, is exactly the shape that earns the capped
// dormant boost — and it would clear the stored ETag/Last-Modified pair,
// turning the next fetch of a briefly unreachable feed into a full body.
//
// Items is carried through for the same reason it exists. feedstate
// records LastDocItems from it and LastDocDated from len(PubDates); a 200
// whose items are all undated must arrive as "4 items, 0 dated", because
// "0 items, 0 dated" is the genuinely quiet feed that keeps the boost.
// Get that wrong and one badly dated feed takes maximum cadence weight on
// items that also take fetch time as their timestamp, i.e. maximum
// recency, on every render.
//
// DatesKnown is the inverse of NotModified: a 304 brought back no
// document, so feedstate keeps the dates and counts it already holds and
// re-windows them at read time.
func observations(results []fetch.FeedResult) []feedstate.Observation {
	obs := make([]feedstate.Observation, 0, len(results))
	for _, r := range results {
		obs = append(obs, feedstate.Observation{
			URL:          r.URL,
			PubDates:     r.ItemDates,
			Items:        r.Items,
			DatesKnown:   !r.NotModified,
			ETag:         r.ETag,
			LastModified: r.LastModified,
		})
	}
	return obs
}

// mergeFollowingStories combines the previous following.json contents with
// what this refresh actually fetched, deciding per feed rather than
// replacing the file wholesale the way the news pool does (see the
// full-replace note beside runDefault's writeCache call). The divergence is
// deliberate: a conditional GET that answers 304 returns no stories at all,
// and all-304 is the steady state for a healthy feed set — writing back
// only what came back would delete every story the user has on the first
// quiet refresh.
//
// Per feed: one that answered 200 (a result with NotModified false) has its
// stories replaced outright, including replaced by nothing when its
// document went empty; one that answered 304, or failed and so reported no
// result at all, keeps whatever the cache already held for it; and one no
// longer in configured is dropped whether or not it was fetched. Output is
// emitted in configured order so the written file is stable between
// refreshes rather than reordered by goroutine completion.
func mergeFollowingStories(cached, fetched []fetch.Story, results []fetch.FeedResult, configured []string) []fetch.Story {
	refreshed := make(map[string]bool, len(results))
	for _, r := range results {
		if !r.NotModified {
			refreshed[r.URL] = true
		}
	}
	cachedByFeed := make(map[string][]fetch.Story, len(configured))
	for _, s := range cached {
		cachedByFeed[s.Feed] = append(cachedByFeed[s.Feed], s)
	}
	fetchedByFeed := make(map[string][]fetch.Story, len(configured))
	for _, s := range fetched {
		fetchedByFeed[s.Feed] = append(fetchedByFeed[s.Feed], s)
	}
	out := make([]fetch.Story, 0, len(cached)+len(fetched))
	for _, url := range configured {
		if refreshed[url] {
			out = append(out, fetchedByFeed[url]...)
			continue
		}
		out = append(out, cachedByFeed[url]...)
	}
	return out
}

// newFollowing builds the fan-out client for one following-pool refresh.
// Tests MAY swap this to inject an httptest-backed Client, or to assert the
// fan-out is never constructed at all, but MUST restore via
// t.Cleanup(func() { newFollowing = original }) so the swap cannot leak
// into another test — the same contract newSource and spawnRefresh carry in
// main.go.
//
// Sanctioned exception to the no-global-mutable-state convention
// (CLAUDE.md, ruled 2026-08-26): package-main test seams, swapped only in
// tests, restored via t.Cleanup.
var newFollowing = func(specs []fetch.FeedSpec, validators func(string) (string, string)) *fetch.Following {
	return &fetch.Following{
		Feeds:          specs,
		Validators:     validators,
		PerFeedTimeout: defaults.FollowingPerFeedTimeout,
	}
}

// refreshFollowing fetches every configured feed and rewrites
// following.json and feeds.json. It runs only inside the detached
// --__refresh process: the 15s parent budget below is deliberately three
// times the render path's, which is why nothing on a render path may call
// it (design addendum item 7).
//
// The order here is load-bearing end to end:
//
//   - Stored validators are read BEFORE the fetch, or every feed answers
//     200 with a full body instead of a cheap 304 — but following.json is
//     read before them, because a stored validator is only safe to send
//     for a feed the cache still holds a story for. A 304 returns no
//     stories and the merge below keeps a 304 feed's CACHED ones, so
//     sending an ETag for a feed the cache no longer covers writes an
//     empty result back and does the same on every later refresh, leaving
//     the pool permanently empty until the publisher happens to change the
//     document. feeds.json and following.json have independent lifetimes,
//     and R-6's piped --uninstall removes the caches while deliberately
//     keeping the state directory, so that state is one documented command
//     away. Hence the per-feed suppression below (ruling R-37): feeds the
//     cache still covers keep their 304s, and only the feeds that need a
//     full 200 pay for one.
//   - The 15s parent is defaults.FollowingFetchTimeout, NOT
//     defaults.FetchTimeout: a 5s parent would silently clip each feed's
//     10s sub-timeout to 5s, so slow-but-working feeds would start failing
//     for a reason no log line would name.
//   - Per-feed errors are logged under a "following <url>: " prefix.
//     FetchFeeds keys its error map by feed URL while FetchAll keys its own
//     by source name, so the two must never be merged into one loop — the
//     result would mislabel every warning (ruling R-30).
//   - feedstate.Update runs AFTER the cache write and takes the SAME now
//     the cache recorded, so freshness and cadence weights never disagree
//     about when this refresh happened. Its failure is logged, not
//     returned: a wedged feeds.lock costs the user a cadence update, and it
//     must not also cost them the stories already fetched.
//
// A pool that is not active is skipped whole rather than refreshed with an
// empty feed list, because feedstate.Update garbage-collects every feed
// absent from configured — one refresh with the pool disabled would wipe
// four weeks of cadence observations and every stored validator, breaking
// the design's persist-don't-clear promise.
//
// An error means the pool genuinely failed (every feed errored, or a path
// or write failure); a skip and a partial success both return nil. Callers
// count those errors to decide the refresh process's exit status.
func refreshFollowing(ctx context.Context, cfg config.Config, now time.Time) error {
	if !cfg.FollowingActive() {
		return nil
	}
	fsPath, err := feedstate.Path()
	if err != nil {
		return fmt.Errorf("feedstate path: %w", err)
	}
	// A corrupt or unreadable store is not a refresh failure: it costs this
	// pass its conditional GETs, and Update rebuilds the file from scratch.
	state, err := feedstate.Read(fsPath)
	if err != nil {
		state = &feedstate.File{Version: feedstate.SchemaVersion}
	}
	path, err := cache.PoolPath("following")
	if err != nil {
		return fmt.Errorf("following cache path: %w", err)
	}
	// A cache that will not read means no cached stories at all, which is
	// not a refresh failure — it is precisely the situation R-37 covers,
	// and every feed below simply rebuilds from a full 200.
	var cached []fetch.Story
	if f, err := cache.Read(path); err == nil {
		cached = f.Stories
	}
	// Story.Feed is the attribution key the merge decides on, so it is the
	// key coverage is measured with too.
	covered := make(map[string]bool, len(cached))
	for _, s := range cached {
		covered[s.Feed] = true
	}
	// state.Validators already has the signature fetch.Following wants; the
	// ONLY reason for this wrapper is R-37's per-feed suppression, so a
	// feed with nothing left in the cache asks for a full document instead
	// of a 304 that would keep nothing. Do not "simplify" it back to
	// passing state.Validators straight through. Keys are the configured
	// feed URL on both sides, which is what keeps them from drifting.
	validators := func(url string) (etag, lastModified string) {
		if !covered[url] {
			return "", ""
		}
		return state.Validators(url)
	}
	client := newFollowing(cfg.FeedSpecs(), validators)

	fctx, cancel := context.WithTimeout(ctx, defaults.FollowingFetchTimeout)
	defer cancel()
	fetched, results, errs := client.FetchFeeds(fctx)
	for url, e := range errs {
		_ = refreshlog.Append(fmt.Sprintf("following %s: %s", url, e))
	}
	if len(results) == 0 {
		if len(errs) > 0 {
			// Nothing succeeded, so there is no new truth to write: the
			// stale file and its old FetchedAt both stand.
			return fmt.Errorf("all %d feeds failed", len(errs))
		}
		return nil
	}

	configured := cfg.FeedURLs()
	merged := mergeFollowingStories(cached, fetched, results, configured)
	if err := writeCache(path, merged, now); err != nil {
		return fmt.Errorf("write following cache: %w", err)
	}
	if err := feedstate.Update(fsPath, configured, observations(results), now); err != nil {
		_ = refreshlog.Append(fmt.Sprintf("following feedstate: %s", err))
	}
	return nil
}
