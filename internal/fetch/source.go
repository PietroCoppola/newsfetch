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
	// M2's scoring treats empty Tags as "no tag matches"; M4 lands the
	// 1.5x tags-overlap scoring branch when Lobste.rs is wired up.
	Tags []string `json:"tags"`
}

// FetchOptions carries per-call tuning for [Source.Fetch]. The zero value
// means "whatever the source considers reasonable".
type FetchOptions struct {
	// MinPoints, if > 0, filters to stories with at least this many points.
	MinPoints int
	// Limit, if > 0, caps the number of stories returned. Zero means the
	// source's own default.
	Limit int
}

// Source is the abstraction over a news provider. See the package doc for the
// full contract.
type Source interface {
	Name() string
	Fetch(ctx context.Context, opts FetchOptions) ([]Story, error)
}
