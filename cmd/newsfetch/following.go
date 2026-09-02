package main

import (
	"github.com/PietroCoppola/newsfetch/internal/feedstate"
	"github.com/PietroCoppola/newsfetch/internal/fetch"
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
