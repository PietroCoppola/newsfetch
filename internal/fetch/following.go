package fetch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

const (
	defaultPerFeedTimeout = 10 * time.Second
	defaultMaxItems       = 3
	maxFeedBody           = 5 << 20
)

// FeedSpec is one configured feed: its URL and how many of its newest items
// to keep for the pool. MaxItems <= 0 falls back to 3.
type FeedSpec struct {
	URL      string
	MaxItems int
}

// FeedResult is one feed's outcome from a single FetchFeeds call: the
// conditional-GET validators to persist, and (unless unchanged) the publish
// dates of every dated item the document carried. Part 2's refresh pipeline
// persists this into feedstate; it never sees the parsed stories directly.
type FeedResult struct {
	URL string
	// ItemDates holds the publish time of every dated item in the fetched
	// document, uncapped by MaxItems — the cadence rate feedstate computes
	// from this describes the feed itself, not the subset kept for the
	// pool. Nil when NotModified is true.
	ItemDates    []time.Time
	ETag         string
	LastModified string
	// NotModified is true on a 304: a successful, unchanged fetch. No
	// stories, ItemDates nil, and ETag/LastModified carry the server's
	// refreshed validators, or fall back to the ones this fetch sent when
	// the 304 response didn't resend them.
	NotModified bool
}

// Following is a stateless fan-out client that fetches every configured RSS
// or Atom feed concurrently, one goroutine per feed under its own
// PerFeedTimeout sub-context of the caller's ctx (design doc's §Per-feed
// sub-timeouts: one slow feed must never block or fail the rest). It
// carries no state between calls; conditional-GET validators come in
// through the Validators field rather than a stored cache, which is what
// keeps package fetch a leaf — Following does not import internal/feedstate.
//
// Following deliberately does NOT implement [Source] and has no
// Fetch(ctx, opts) ([]Story, error) adapter (design doc addendum §14): the
// Source contract — never a partial slice with a non-nil error, and an
// empty slice meaning an empty fetch — cannot express a 304 (unchanged is
// not the same as empty) or per-feed partial failure, so any adapter would
// be lossy exactly where the following pool's refresh pipeline needs
// fidelity. FetchFeeds is the only entry point; the caller owns cache
// semantics (a 304 leaves the existing cache file untouched).
type Following struct {
	Feeds []FeedSpec
	// Client is the HTTP client used for every feed request. Nil means
	// http.DefaultClient.
	Client *http.Client
	// Validators supplies the conditional-GET headers to send for a given
	// feed URL, read from persisted feedstate by the caller. Nil means no
	// conditional GET is attempted (always a full fetch).
	Validators func(url string) (etag, lm string)
	// PerFeedTimeout bounds each feed's request as a sub-context of the
	// caller's ctx. Zero means 10s.
	PerFeedTimeout time.Duration
}

// Name labels stories and errors from this fetcher ("following").
// Following is deliberately not a Source — see the type doc.
func (f *Following) Name() string { return "following" }

// FetchFeeds fans out one goroutine per configured feed, each under its
// own PerFeedTimeout (mirroring FetchAll's shape in multi.go: mutex-
// guarded appends, feedErrs nil when empty, completion order). One slow
// or broken feed never blocks or fails the rest; the caller reads the
// three outputs together — stories to merge, results to persist into
// feedstate, errors for the warning footer.
func (f *Following) FetchFeeds(ctx context.Context) ([]Story, []FeedResult, map[string]error) {
	if len(f.Feeds) == 0 {
		return nil, nil, nil
	}
	timeout := f.PerFeedTimeout
	if timeout <= 0 {
		timeout = defaultPerFeedTimeout
	}
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		stories []Story
		results []FeedResult
	)
	errs := make(map[string]error)
	wg.Add(len(f.Feeds))
	for _, spec := range f.Feeds {
		spec := spec
		go func() {
			defer wg.Done()
			fctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			res, got, err := f.fetchOne(fctx, spec)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs[spec.URL] = err
				return
			}
			results = append(results, res)
			stories = append(stories, got...)
		}()
	}
	wg.Wait()
	if len(errs) == 0 {
		errs = nil
	}
	return stories, results, errs
}

func (f *Following) fetchOne(ctx context.Context, spec FeedSpec) (FeedResult, []Story, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, spec.URL, nil)
	if err != nil {
		return FeedResult{}, nil, fmt.Errorf("feed %s: %w", spec.URL, err)
	}
	req.Header.Set("User-Agent", userAgent())
	var sentETag, sentLM string
	if f.Validators != nil {
		sentETag, sentLM = f.Validators(spec.URL)
		if sentETag != "" {
			req.Header.Set("If-None-Match", sentETag)
		}
		if sentLM != "" {
			req.Header.Set("If-Modified-Since", sentLM)
		}
	}
	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return FeedResult{}, nil, fmt.Errorf("feed %s: %w", spec.URL, err)
	}
	defer resp.Body.Close()

	res := FeedResult{
		URL:          spec.URL,
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
	}
	if resp.StatusCode == http.StatusNotModified {
		// 304 is a successful unchanged fetch: keep whichever validators
		// the server did not resend.
		res.NotModified = true
		if res.ETag == "" {
			res.ETag = sentETag
		}
		if res.LastModified == "" {
			res.LastModified = sentLM
		}
		return res, nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return FeedResult{}, nil, fmt.Errorf("feed %s: status %d", spec.URL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFeedBody+1))
	if err != nil {
		return FeedResult{}, nil, fmt.Errorf("feed %s: read body: %w", spec.URL, err)
	}
	if len(body) > maxFeedBody {
		return FeedResult{}, nil, fmt.Errorf("feed %s: body exceeds %d bytes", spec.URL, maxFeedBody)
	}
	items, err := parseFeed(body)
	if err != nil {
		return FeedResult{}, nil, fmt.Errorf("feed %s: %w", spec.URL, err)
	}

	fetchedAt := time.Now().UTC()
	for _, it := range items {
		if it.HasDate {
			res.ItemDates = append(res.ItemDates, it.Published)
		}
	}
	// Newest first; undated items count as fetch time, i.e. newest.
	sort.SliceStable(items, func(i, j int) bool {
		return itemTime(items[i], fetchedAt).After(itemTime(items[j], fetchedAt))
	})
	max := spec.MaxItems
	if max <= 0 {
		max = defaultMaxItems
	}
	if len(items) > max {
		items = items[:max]
	}
	stories := make([]Story, 0, len(items))
	for _, it := range items {
		stories = append(stories, Story{
			ID:        "rss-" + it.URL,
			Title:     it.Title,
			URL:       it.URL,
			Source:    "following",
			Author:    it.Author,
			CreatedAt: itemTime(it, fetchedAt),
			Tags:      append([]string{}, it.Tags...),
			Summary:   it.Summary,
			Feed:      spec.URL,
		})
	}
	return res, stories, nil
}

// itemTime is the item's effective timestamp: its pubDate, or the fetch
// time when the feed carries none (HasDate false).
func itemTime(it feedItem, fetchedAt time.Time) time.Time {
	if it.HasDate {
		return it.Published
	}
	return fetchedAt
}
