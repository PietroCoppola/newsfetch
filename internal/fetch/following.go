package fetch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	// ItemDates holds the publish time of every usable dated item in the
	// fetched document, uncapped by MaxItems — the cadence rate feedstate
	// computes from this describes the feed itself, not the subset kept
	// for the pool. Items the parser rejected (no title, no link) and
	// items whose link is not an absolute http(s) URL are not items, so
	// they do not count. Nil when NotModified is true.
	ItemDates []time.Time
	// Items is how many items the document carried in total, dated or
	// not, over exactly ItemDates' scope: after the URL guard, before the
	// MaxItems cap. feedstate needs the total to tell a document that
	// dated nothing (no cadence signal — those items also take fetch time
	// as their timestamp) from one that held nothing (a quiet feed, which
	// keeps its dormant boost). Zero when NotModified is true.
	Items        int
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
//
// The returned error map is keyed by FEED URL. [FetchAll]'s map has the
// same type and shape but is keyed by SOURCE NAME ("hackernews"), so the
// two are not interchangeable: merging them without re-keying would mix
// namespaces and mislabel every warning. Keep them separate.
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
	// Item links may be relative — legal in Atom (RFC 4287 resolves them
	// against the feed URL) and common in RSS too — so resolve them here,
	// the only layer holding the base URL; the parser has none. Anything
	// that does not resolve to an absolute http(s) URL is dropped whole,
	// dates included: a link the renderer cannot open is not an item, and
	// a non-http URL blanks Story.NormalisedHost, silently disabling the
	// diversity host penalty.
	//
	// The base is the FINAL URL after redirects, which http.Client records
	// as resp.Request, not the configured one: a feed that redirects into
	// another directory serves links relative to where the document
	// actually came from, so resolving against the configured URL points
	// them at the wrong directory. Only the base moves — FeedResult.URL
	// and Story.Feed stay keyed by the configured URL, which is what the
	// config lists and what feedstate stores.
	base := req.URL
	if resp.Request != nil && resp.Request.URL != nil {
		base = resp.Request.URL
	}
	resolved := make([]feedItem, 0, len(items))
	for _, it := range items {
		abs, ok := resolveItemURL(base, it.URL)
		if !ok {
			continue
		}
		it.URL = abs
		resolved = append(resolved, it)
	}
	items = resolved

	fetchedAt := time.Now().UTC()
	res.Items = len(items)
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

// resolveItemURL resolves link against base and reports whether the
// result is a usable absolute http(s) URL.
//
// An http(s) scheme is not on its own enough to be usable. "http:post" is
// opaque and "https:///post" has an empty authority; both carry an
// accepted scheme, resolve to no host at all, and reach the renderer as
// dead links whose NormalisedHost is empty — which quietly turns off the
// diversity host penalty for them. Hostname(), not Host: "https://:8080/x"
// has a non-empty Host (the port) and no host name. The Opaque test is
// redundant once a host name is required, but states the intent.
func resolveItemURL(base *url.URL, link string) (string, bool) {
	ref, err := url.Parse(link)
	if err != nil {
		return "", false
	}
	abs := base.ResolveReference(ref)
	if abs.Scheme != "http" && abs.Scheme != "https" {
		return "", false
	}
	if abs.Opaque != "" || abs.Hostname() == "" {
		return "", false
	}
	return abs.String(), true
}

// itemTime is the item's effective timestamp: its pubDate, or the fetch
// time when the feed carries none (HasDate false).
func itemTime(it feedItem, fetchedAt time.Time) time.Time {
	if it.HasDate {
		return it.Published
	}
	return fetchedAt
}
