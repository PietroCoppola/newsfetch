package fetch

import (
	"context"
	"time"
)

// Story is the unit of news produced by a [Source] and persisted in the cache.
// Renaming a field is a cache-schema change; bump the cache schema version in
// that case.
type Story struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Source    string    `json:"source"`
	Points    int       `json:"points"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
	// Tags are source-provided topic tags (e.g. from Lobste.rs). HN does
	// not expose topic tags so HackerNews.Fetch populates an empty slice.
	// The ranker matches topics against title and tags uniformly — see
	// rank.matchesAnyTopic.
	Tags []string `json:"tags"`
}

// FetchOptions carries per-call tuning for [Source.Fetch]. The zero value
// means "whatever the source considers reasonable".
type FetchOptions struct {
	// MinPoints is source-advisory: sources may ignore it. M4's stance
	// works because the currently-shipping sources (HN and Lobste.rs)
	// are both curated — HN by community voting, Lobste.rs by submission
	// rules and moderation — so a "score floor" is meaningful only for
	// sources that have a comparable signal at a comparable scale. HN
	// honours MinPoints (its scores run 100-2000+); Lobste.rs ignores
	// it (scores run 1-100, and its hottest list is already curated).
	// When uncurated sources land in M5 (RSS especially), per-source
	// quality knobs may need to be designed in TOML — see spec.md §15.
	MinPoints int
	// Limit, if > 0, caps the number of stories returned. Zero means the
	// source's own default. Like MinPoints, sources may ignore this if
	// the upstream API offers no equivalent.
	Limit int
}

// Source is the abstraction over a news provider. See the package doc for the
// full contract.
type Source interface {
	Name() string
	Fetch(ctx context.Context, opts FetchOptions) ([]Story, error)
}

// KnownSourceNames lists every Source name the binary recognises. Single
// source of truth: the config validator uses it to flag unknown source
// names, and cmd/newsfetch's factory uses it to instantiate Sources by
// name. Add new sources here when they ship.
var KnownSourceNames = []string{"hackernews", "lobsters"}
