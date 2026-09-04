package onboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ReadSettingsJSON parses [Answers] from r as JSON for --settings. Schema:
//
//	{ "topics": ["rust"], "style": "boxed", "pools": ["news", "following"],
//	  "pool_order": ["following", "news"], "count": 3, "following_count": 1,
//	  "ticker_marker": "branch", "ticker_boxed": true,
//	  "cache_ttl_minutes": 45, "min_points": 10, "dedup_ttl_hours": 3,
//	  "news": {"aggregators": ["hackernews"]},
//	  "following": {"feeds": [{"url": "https://example.com/feed.xml"}]} }
//
// topics, style, pools, and count are required — --settings is the
// edit-everything contract, in contrast with --init's onboarding contract
// where most fields are optional. count must be in [1, MaxCount].
//
// Everything else is OPTIONAL and inherits from current when omitted:
// pool_order, following_count, news.aggregators, following.feeds,
// ticker_marker, ticker_boxed, cache_ttl_minutes, min_points, and
// dedup_ttl_hours. That is persist-don't-clear, and here it is a
// data-safety rule rather than a convenience — the Answers this function
// returns are written over the user's ENTIRE config file, so a field that
// does not survive an omission is a field deleted from disk. The per-feed
// max_items and weight knobs are the sharpest case: the wizard never
// surfaces them, so an omitted following.feeds must hand back current's
// feeds with their pointers untouched. cache_ttl_minutes, min_points, and
// dedup_ttl_hours are never surfaced by either wizard at all, so the same
// rule applies to all three: an omission is not a request to revert to the
// compile-time default, it means the caller did not touch the field.
//
// An explicitly empty array is not an omission: "feeds": [] clears every
// feed, which is the only way a scripted caller can unsubscribe from
// everything.
//
// Invalid values are rejected outright rather than clamped — JSON callers
// fail loud. That includes the cross-field rule: if the merged result would
// leave every enabled pool empty, this returns an error rather than falling
// back to the defaults the way config.Validate does for a TOML file.
// Unknown fields are rejected at every nesting depth.
//
// There is NO "sources" key and no alias for one (ruling R-4); see
// ReadInitJSON's doc comment for why the TOML loader still aliases it and
// this reader deliberately does not.
func ReadSettingsJSON(r io.Reader, current Answers) (Answers, error) {
	var raw struct {
		Topics          *[]string      `json:"topics"`
		Style           *string        `json:"style"`
		Pools           *[]string      `json:"pools"`
		PoolOrder       *[]string      `json:"pool_order"`
		Count           *int           `json:"count"`
		FollowingCount  *int           `json:"following_count"`
		TickerMarker    *string        `json:"ticker_marker"`
		TickerBoxed     *bool          `json:"ticker_boxed"`
		CacheTTLMinutes *int           `json:"cache_ttl_minutes"`
		MinPoints       *int           `json:"min_points"`
		DedupTTLHours   *int           `json:"dedup_ttl_hours"`
		News            *newsJSON      `json:"news"`
		Following       *followingJSON `json:"following"`
	}
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return Answers{}, fmt.Errorf("decode --settings JSON: %w", err)
	}
	if raw.Topics == nil {
		return Answers{}, errors.New(`--settings JSON: missing required field "topics" (array of strings; [] is allowed)`)
	}
	if raw.Style == nil {
		return Answers{}, errors.New(`--settings JSON: missing required field "style" (boxed | minimal | json)`)
	}
	if raw.Pools == nil {
		return Answers{}, errors.New(`--settings JSON: missing required field "pools" (non-empty array of pool names)`)
	}
	if raw.Count == nil {
		return Answers{}, errors.New(`--settings JSON: missing required field "count" (1..4)`)
	}
	if err := validateStyle("--settings", *raw.Style); err != nil {
		return Answers{}, err
	}
	if err := validatePools("--settings", *raw.Pools); err != nil {
		return Answers{}, err
	}
	if err := validateCount("--settings", *raw.Count); err != nil {
		return Answers{}, err
	}
	a := Answers{
		Topics:          *raw.Topics,
		Style:           *raw.Style,
		Pools:           *raw.Pools,
		PoolOrder:       current.PoolOrder,
		NewsAggregators: current.NewsAggregators,
		Count:           *raw.Count,
		FollowingCount:  current.FollowingCount,
		Feeds:           current.Feeds,
		TickerMarker:    current.TickerMarker,
		TickerBoxed:     current.TickerBoxed,
		CacheTTLMinutes: current.CacheTTLMinutes,
		MinPoints:       current.MinPoints,
		DedupTTLHours:   current.DedupTTLHours,
	}
	// pool_order is validated only when the caller supplied it. An inherited
	// order was already valid when it was written, and config.Validate
	// repairs one that a pool change has left partial — rejecting the
	// caller's payload over a field they did not send would be blaming the
	// wrong edit.
	if raw.PoolOrder != nil {
		if err := validatePoolOrder("--settings", *raw.PoolOrder, a.Pools); err != nil {
			return Answers{}, err
		}
		a.PoolOrder = *raw.PoolOrder
	}
	if raw.FollowingCount != nil {
		if err := validateFollowingCount("--settings", *raw.FollowingCount); err != nil {
			return Answers{}, err
		}
		a.FollowingCount = *raw.FollowingCount
	}
	if raw.TickerMarker != nil {
		if err := validateTickerMarker("--settings", *raw.TickerMarker); err != nil {
			return Answers{}, err
		}
		a.TickerMarker = *raw.TickerMarker
	}
	if raw.TickerBoxed != nil {
		a.TickerBoxed = *raw.TickerBoxed
	}
	// Validated only when the caller supplied them, for the same reason
	// pool_order is: an inherited value came off the user's existing config,
	// where config.Validate clamps it at render time, and failing the save
	// over a field the payload never mentioned would blame the wrong edit
	// and leave no way to fix anything else through --settings.
	if raw.CacheTTLMinutes != nil {
		if err := validateCacheTTLMinutes("--settings", *raw.CacheTTLMinutes); err != nil {
			return Answers{}, err
		}
		a.CacheTTLMinutes = *raw.CacheTTLMinutes
	}
	if raw.MinPoints != nil {
		if err := validateMinPoints("--settings", *raw.MinPoints); err != nil {
			return Answers{}, err
		}
		a.MinPoints = *raw.MinPoints
	}
	if raw.DedupTTLHours != nil {
		if err := validateDedupTTLHours("--settings", *raw.DedupTTLHours); err != nil {
			return Answers{}, err
		}
		a.DedupTTLHours = *raw.DedupTTLHours
	}
	if raw.News != nil && raw.News.Aggregators != nil {
		if err := validateSources("--settings", *raw.News.Aggregators); err != nil {
			return Answers{}, err
		}
		a.NewsAggregators = *raw.News.Aggregators
	}
	if raw.Following != nil && raw.Following.Feeds != nil {
		feeds, err := feedsFromJSON("--settings", *raw.Following.Feeds)
		if err != nil {
			return Answers{}, err
		}
		if err := validateFeeds("--settings", feeds); err != nil {
			return Answers{}, err
		}
		a.Feeds = feeds
	}
	// Last, and only here: a --settings payload can be individually valid in
	// every field and still describe a config that renders nothing, because
	// the emptiness arrives through inheritance from current rather than
	// through the payload.
	if err := validatePoolContent("--settings", a); err != nil {
		return Answers{}, err
	}
	return a, nil
}
