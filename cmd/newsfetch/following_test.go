package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/cache"
	"github.com/PietroCoppola/newsfetch/internal/config"
	"github.com/PietroCoppola/newsfetch/internal/feedstate"
	"github.com/PietroCoppola/newsfetch/internal/fetch"
)

// TestObservations_MapsEveryField walks every FeedResult shape FetchFeeds
// can produce. Each field is load-bearing somewhere in feedstate — dates
// drive the rate, Items and the dated count decide whether a rate means
// anything at all, the validators decide whether the next fetch costs a
// body — so the table asserts the whole struct rather than field by field.
func TestObservations_MapsEveryField(t *testing.T) {
	first := time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC)
	second := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	const lm = "Mon, 31 Aug 2026 09:00:00 GMT"

	cases := []struct {
		name string
		in   fetch.FeedResult
		want feedstate.Observation
	}{
		{
			name: "a 200 with dated items reports dates, total items and validators",
			in: fetch.FeedResult{
				URL:          "https://a.example/feed",
				ItemDates:    []time.Time{first, second},
				Items:        3,
				ETag:         `"v1"`,
				LastModified: lm,
			},
			want: feedstate.Observation{
				URL:          "https://a.example/feed",
				PubDates:     []time.Time{first, second},
				Items:        3,
				DatesKnown:   true,
				ETag:         `"v1"`,
				LastModified: lm,
			},
		},
		{
			// The dangerous row. Items=4 with no dates is what feedstate
			// needs to record "this document dated nothing" instead of
			// "this document held nothing" — see
			// TestObservations_UndatedDocumentMustNotEarnTheDormantBoost.
			name: "a 200 whose items are all undated still reports the item count",
			in: fetch.FeedResult{
				URL:   "https://b.example/feed",
				Items: 4,
				ETag:  `"v2"`,
			},
			want: feedstate.Observation{
				URL:        "https://b.example/feed",
				Items:      4,
				DatesKnown: true,
				ETag:       `"v2"`,
			},
		},
		{
			name: "a 200 with zero items is a known-empty document",
			in: fetch.FeedResult{
				URL:  "https://c.example/feed",
				ETag: `"v3"`,
			},
			want: feedstate.Observation{
				URL:        "https://c.example/feed",
				DatesKnown: true,
				ETag:       `"v3"`,
			},
		},
		{
			// A 304 brought back no document, so DatesKnown is false and
			// feedstate keeps the dates and counts it already holds. The
			// validators still pass through: the fetcher has already
			// resolved them, preferring the ones the 304 regenerated and
			// back-filling the ones it sent.
			name: "a 304 reports no document but carries the validators",
			in: fetch.FeedResult{
				URL:          "https://d.example/feed",
				NotModified:  true,
				ETag:         `"v4"`,
				LastModified: lm,
			},
			want: feedstate.Observation{
				URL:          "https://d.example/feed",
				DatesKnown:   false,
				ETag:         `"v4"`,
				LastModified: lm,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := observations([]fetch.FeedResult{tc.in})
			if len(got) != 1 {
				t.Fatalf("observations returned %d records, want 1: %+v", len(got), got)
			}
			if !reflect.DeepEqual(got[0], tc.want) {
				t.Errorf("observation mismatch\ngot:  %+v\nwant: %+v", got[0], tc.want)
			}
		})
	}
}

// rssDocXML wraps item XML in a minimal RSS 2.0 channel.
func rssDocXML(items ...string) string {
	return `<rss version="2.0"><channel><title>Test feed</title>` +
		strings.Join(items, "") + `</channel></rss>`
}

// TestObservations_FailedFeedGetsNoObservation runs a real fan-out over an
// httptest server where one feed 500s. A failed feed must leave no trace
// in feedstate: it has no document to describe, and a synthesised
// zero-valued observation would overwrite the stored document shape (which
// reads as dormancy) and clear the conditional-GET validators, turning
// every later fetch of a temporarily unreachable feed into a full body.
func TestObservations_FailedFeedGetsNoObservation(t *testing.T) {
	body := rssDocXML(fmt.Sprintf(
		"<item><title>Good item</title><link>https://example.com/good</link><description>d</description><pubDate>%s</pubDate></item>",
		time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC1123Z),
	))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	good, bad := srv.URL+"/good", srv.URL+"/bad"
	f := &fetch.Following{
		Feeds:          []fetch.FeedSpec{{URL: good}, {URL: bad}},
		Client:         srv.Client(),
		PerFeedTimeout: 2 * time.Second,
	}
	stories, results, errs := f.FetchFeeds(context.Background())
	if len(stories) != 1 {
		t.Fatalf("stories = %d, want 1 (only the reachable feed contributes)", len(stories))
	}
	if errs[bad] == nil {
		t.Fatalf("errs = %v, want an entry for the 500 feed", errs)
	}

	obs := observations(results)
	if len(obs) != 1 {
		t.Fatalf("observations = %d records, want 1: %+v", len(obs), obs)
	}
	if obs[0].URL != good {
		t.Errorf("observation URL = %q, want %q", obs[0].URL, good)
	}
	if !obs[0].DatesKnown || obs[0].Items != 1 || len(obs[0].PubDates) != 1 {
		t.Errorf("observation = %+v, want the good feed's one dated item with DatesKnown", obs[0])
	}
	for _, o := range obs {
		if o.URL == bad {
			t.Errorf("an observation was synthesised for the failed feed: %+v", o)
		}
	}
}

// TestObservations_UndatedDocumentMustNotEarnTheDormantBoost is the pin
// for the one field this mapping must never drop: Items.
//
// feedstate records LastDocItems from Observation.Items and LastDocDated
// from len(PubDates). Drop Items and a 200 whose items are all undated
// arrives as "0 items, 0 dated" — which, on a feed whose EverDated latch
// is already set, is the genuinely-quiet-feed shape that takes the capped
// maximum cadence boost. Those same undated items also take fetch time as
// their timestamp inside internal/fetch, i.e. maximum recency. Max boost
// times max recency, on every render, from one feed with a broken date
// format. Carrying Items makes it "3 items, 0 dated": no cadence signal,
// neutral 1.0, and excluded from the corpus median.
func TestObservations_UndatedDocumentMustNotEarnTheDormantBoost(t *testing.T) {
	const week = 7 * 24 * time.Hour
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	writeAt := now.Add(-8 * week) // 8 weeks of history: confidence 1 at `now`
	path := filepath.Join(t.TempDir(), "feeds.json")
	const probe, anchor = "https://probe.example/feed", "https://anchor.example/feed"
	cfg := []string{probe, anchor}

	// Pass 1: the probe dates one item, latching EverDated; that date has
	// aged out of the 4-week window by `now`. The anchor's four dates are
	// in the future at write time (the write-prune keeps those) and
	// in-window at `now`, pinning its rate at 1.0/wk there.
	if err := feedstate.Update(path, cfg, observations([]fetch.FeedResult{
		{URL: probe, ItemDates: []time.Time{writeAt.Add(-time.Hour)}, Items: 1},
		{URL: anchor, ItemDates: []time.Time{
			now.Add(-1 * time.Hour), now.Add(-2 * time.Hour),
			now.Add(-3 * time.Hour), now.Add(-4 * time.Hour),
		}, Items: 4},
	}), writeAt); err != nil {
		t.Fatalf("first Update: %v", err)
	}
	// Pass 2: the probe serves a 200 with three items and not one parseable
	// date.
	if err := feedstate.Update(path, cfg, observations([]fetch.FeedResult{
		{URL: probe, Items: 3},
	}), writeAt.Add(time.Hour)); err != nil {
		t.Fatalf("second Update: %v", err)
	}

	f, err := feedstate.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	w := f.Weights(cfg, now)
	if got := w[probe]; math.Abs(got-1.0) > 1e-9 {
		t.Errorf("undated 200 weight = %v, want neutral 1.0 (a document that dated nothing has no cadence signal; the cap here would be max boost on items that also take fetch time as their timestamp)", got)
	}
	// A feed with no cadence signal must also stay out of the corpus
	// median: a rate it never reported is not a zero to average in. With
	// the anchor alone in the corpus its own rate is the reference, so it
	// is exactly neutral.
	if got := w[anchor]; math.Abs(got-1.0) > 1e-9 {
		t.Errorf("anchor = %v, want 1.0 (a signal-less probe must not drag the corpus median down)", got)
	}
}

// storyFor builds a following-pool story attributed to feed, matching the
// shape fetch.Following produces (ID "rss-"+URL, Source "following",
// Feed set to the CONFIGURED url).
func storyFor(feed, url string) fetch.Story {
	return fetch.Story{
		ID:     "rss-" + url,
		Title:  url,
		URL:    url,
		Source: "following",
		Feed:   feed,
	}
}

func storyURLs(stories []fetch.Story) []string {
	out := make([]string, 0, len(stories))
	for _, s := range stories {
		out = append(out, s.URL)
	}
	return out
}

// TestMergeFollowingStories pins ruling R-22: following.json merges per
// feed instead of full-replacing like the news pool. A 304 returns no
// stories at all and all-304 is the steady state, so "write back only what
// FetchFeeds returned" would silently delete every story the user has.
func TestMergeFollowingStories(t *testing.T) {
	const (
		feedA = "https://a.example.com/feed.xml"
		feedB = "https://b.example.com/feed.xml"
		feedC = "https://c.example.com/feed.xml"
		gone  = "https://gone.example.com/feed.xml"
	)
	cases := []struct {
		name       string
		cached     []fetch.Story
		fetched    []fetch.Story
		results    []fetch.FeedResult
		configured []string
		want       []string
	}{
		{
			name: "200 replaces, 304 keeps, errored keeps",
			cached: []fetch.Story{
				storyFor(feedA, "https://a.example.com/old"),
				storyFor(feedB, "https://b.example.com/old"),
				storyFor(feedC, "https://c.example.com/old"),
			},
			// feedC errored: it is absent from both fetched and results.
			fetched: []fetch.Story{storyFor(feedA, "https://a.example.com/new")},
			results: []fetch.FeedResult{
				{URL: feedA, Items: 1},
				{URL: feedB, NotModified: true},
			},
			configured: []string{feedA, feedB, feedC},
			want: []string{
				"https://a.example.com/new",
				"https://b.example.com/old",
				"https://c.example.com/old",
			},
		},
		{
			name: "feed dropped from config loses its cached stories",
			cached: []fetch.Story{
				storyFor(feedA, "https://a.example.com/old"),
				storyFor(gone, "https://gone.example.com/one"),
				storyFor(gone, "https://gone.example.com/two"),
			},
			fetched:    nil,
			results:    []fetch.FeedResult{{URL: feedA, NotModified: true}},
			configured: []string{feedA},
			want:       []string{"https://a.example.com/old"},
		},
		{
			name:    "200 with an empty document clears that feed",
			cached:  []fetch.Story{storyFor(feedA, "https://a.example.com/old")},
			fetched: nil,
			results: []fetch.FeedResult{{URL: feedA, Items: 0}},

			configured: []string{feedA},
			want:       []string{},
		},
		{
			name:       "all 304 keeps everything, in configured order",
			cached:     []fetch.Story{storyFor(feedB, "https://b.example.com/old"), storyFor(feedA, "https://a.example.com/old")},
			fetched:    nil,
			results:    []fetch.FeedResult{{URL: feedA, NotModified: true}, {URL: feedB, NotModified: true}},
			configured: []string{feedA, feedB},
			want:       []string{"https://a.example.com/old", "https://b.example.com/old"},
		},
		{
			name:       "unattributed cached story is dropped",
			cached:     []fetch.Story{{ID: "hn-1", Title: "aggregator leak", URL: "https://news.example.com/x"}},
			fetched:    []fetch.Story{storyFor(feedA, "https://a.example.com/new")},
			results:    []fetch.FeedResult{{URL: feedA, Items: 1}},
			configured: []string{feedA},
			want:       []string{"https://a.example.com/new"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := storyURLs(mergeFollowingStories(tc.cached, tc.fetched, tc.results, tc.configured))
			if !slices.Equal(got, tc.want) {
				t.Errorf("merged URLs = %v, want %v", got, tc.want)
			}
		})
	}
}

// rssDoc wraps item blocks in a minimal RSS 2.0 document.
func rssDoc(items ...string) string {
	return `<rss version="2.0"><channel><title>Test feed</title>` +
		strings.Join(items, "") + `</channel></rss>`
}

// rssEntry builds one dated <item>. pubDate is RFC 3339 (one of the
// layouts the feed parser accepts) so no weekday-vs-date mismatch can
// make a fixture unparseable.
func rssEntry(title, link, pubDate string) string {
	return fmt.Sprintf(
		"<item><title>%s</title><link>%s</link><description>d</description><pubDate>%s</pubDate></item>",
		title, link, pubDate)
}

// seedFeedState writes feeds.json through the real Update path so the test
// exercises the same storage the refresh reads validators from.
func seedFeedState(t *testing.T, configured []string, obs []feedstate.Observation, at time.Time) string {
	t.Helper()
	path, err := feedstate.Path()
	if err != nil {
		t.Fatalf("feedstate.Path: %v", err)
	}
	if err := feedstate.Update(path, configured, obs, at); err != nil {
		t.Fatalf("seed feedstate: %v", err)
	}
	return path
}

// seedFollowingCache writes following.json directly so the merge has a
// prior state to preserve.
func seedFollowingCache(t *testing.T, stories []fetch.Story, at time.Time) string {
	t.Helper()
	path, err := cache.PoolPath("following")
	if err != nil {
		t.Fatalf("cache.PoolPath: %v", err)
	}
	if err := writeCache(path, stories, at); err != nil {
		t.Fatalf("seed following cache: %v", err)
	}
	return path
}

func feedStateFor(t *testing.T, f *feedstate.File, url string) feedstate.Feed {
	t.Helper()
	for _, fd := range f.Feeds {
		if fd.URL == url {
			return fd
		}
	}
	t.Fatalf("feeds.json has no entry for %s (entries: %d)", url, len(f.Feeds))
	return feedstate.Feed{}
}

// TestRefreshFollowing_MixedFeedOutcomes is the integration guard for the
// whole pipeline: three feeds answering 200 / 304 / 500 in one pass. It
// asserts the merge (R-22), that a failed feed synthesises no observation,
// and that conditional-GET validators round-trip through feeds.json.
//
// It is also R-37's positive case: every feed here has a story in the
// seeded following.json, so every feed is entitled to its stored
// validators and feed B's If-None-Match assertion below must hold. The
// suppression case lives in
// TestRefreshFollowing_MissingCacheRefetchesWithoutValidators.
func TestRefreshFollowing_MixedFeedOutcomes(t *testing.T) {
	isolateXDG(t)

	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	var sawIfNoneMatch string
	mux := http.NewServeMux()
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"a-v2"`)
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, rssDoc(rssEntry("A fresh", "https://a.example.com/new", "2026-09-01T10:00:00Z")))
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) {
		sawIfNoneMatch = r.Header.Get("If-None-Match")
		if sawIfNoneMatch == `"b-v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		// A wrong-validator request must be visibly different from a 304 so
		// a broken conditional-GET wiring fails this test instead of
		// passing by luck.
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, rssDoc(rssEntry("B REFETCHED", "https://b.example.com/refetched", "2026-09-01T11:00:00Z")))
	})
	mux.HandleFunc("/c", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	feedA, feedB, feedC := srv.URL+"/a", srv.URL+"/b", srv.URL+"/c"
	const goneFeed = "https://gone.example.com/feed.xml"

	seedAt := now.Add(-time.Hour)
	fsPath := seedFeedState(t, []string{feedA, feedB, feedC, goneFeed}, []feedstate.Observation{
		{URL: feedA, PubDates: []time.Time{now.Add(-72 * time.Hour)}, Items: 1, DatesKnown: true, ETag: `"a-v1"`},
		{URL: feedB, PubDates: []time.Time{now.Add(-24 * time.Hour)}, Items: 1, DatesKnown: true, ETag: `"b-v1"`},
		{URL: feedC, PubDates: []time.Time{now.Add(-48 * time.Hour), now.Add(-49 * time.Hour)}, Items: 2, DatesKnown: true, ETag: `"c-v1"`},
		// goneFeed is configured *at seed time* and observed, so Update
		// writes a real entry for it. It is absent from cfg.Following
		// below, so the refresh's Update must garbage-collect that entry.
		// Without an observation here Update would never create the entry
		// and the GC assertion at the end would hold vacuously.
		{URL: goneFeed, PubDates: []time.Time{now.Add(-36 * time.Hour)}, Items: 1, DatesKnown: true, ETag: `"gone-v1"`},
	}, seedAt)

	// Precondition for the GC assertion: the entry has to exist before the
	// refresh, or its absence afterwards proves nothing. feedStateFor
	// fatals when it is missing.
	seeded, err := feedstate.Read(fsPath)
	if err != nil {
		t.Fatalf("read seeded feeds.json: %v", err)
	}
	feedStateFor(t, seeded, goneFeed)

	cachePath := seedFollowingCache(t, []fetch.Story{
		storyFor(feedA, "https://a.example.com/old"),
		storyFor(feedB, "https://b.example.com/old"),
		storyFor(feedC, "https://c.example.com/old"),
		storyFor(goneFeed, "https://gone.example.com/old"),
	}, now.Add(-90*time.Minute))

	cfg := config.Defaults()
	cfg.Pools = []string{"news", "following"}
	cfg.Following = config.FollowingConfig{Feeds: []config.FeedConfig{
		{URL: feedA}, {URL: feedB}, {URL: feedC},
	}}

	if err := refreshFollowing(context.Background(), cfg, now); err != nil {
		t.Fatalf("refreshFollowing: %v", err)
	}

	if sawIfNoneMatch != `"b-v1"` {
		t.Errorf("If-None-Match sent to feed B = %q, want %q — stored validators are not reaching the fetcher", sawIfNoneMatch, `"b-v1"`)
	}

	f, err := cache.Read(cachePath)
	if err != nil {
		t.Fatalf("read following.json: %v", err)
	}
	want := []string{
		"https://a.example.com/new", // 200: replaced
		"https://b.example.com/old", // 304: kept
		"https://c.example.com/old", // 500: kept
	}
	if got := storyURLs(f.Stories); !slices.Equal(got, want) {
		t.Errorf("following.json stories = %v, want %v", got, want)
	}
	if !f.FetchedAt.Equal(now) {
		t.Errorf("FetchedAt = %s, want %s", f.FetchedAt, now)
	}

	state, err := feedstate.Read(fsPath)
	if err != nil {
		t.Fatalf("read feeds.json: %v", err)
	}
	if got := feedStateFor(t, state, feedA).ETag; got != `"a-v2"` {
		t.Errorf("feed A ETag = %q, want %q (200 must store the new validator)", got, `"a-v2"`)
	}
	if got := feedStateFor(t, state, feedB).ETag; got != `"b-v1"` {
		t.Errorf("feed B ETag = %q, want %q (a 304 keeps the validator it sent)", got, `"b-v1"`)
	}
	c := feedStateFor(t, state, feedC)
	if c.ETag != `"c-v1"` {
		t.Errorf("feed C ETag = %q, want %q (a failed feed must keep its stored state)", c.ETag, `"c-v1"`)
	}
	if c.LastDocItems != 2 || c.LastDocDated != 2 {
		t.Errorf("feed C counts = %d/%d, want 2/2 — a failed feed must not get a synthesised zero observation", c.LastDocDated, c.LastDocItems)
	}
	for _, fd := range state.Feeds {
		if fd.URL == goneFeed {
			t.Error("feeds.json still lists the unconfigured feed; Update must GC it")
		}
	}
}

// TestRefreshFollowing_MissingCacheRefetchesWithoutValidators pins ruling
// R-37. feeds.json and following.json are separate files with separate
// lifetimes, and R-6's piped --uninstall removes the caches while
// deliberately keeping the state directory — so "validators stored, cache
// gone" is one documented command away, not a corruption fantasy. Send the
// stored ETag anyway and the feed answers 304; a 304 carries no stories;
// the merge keeps nothing for that feed; an empty cache is written back.
// The next refresh repeats it, and the pool stays empty until a publisher
// happens to change a document.
//
// The suppression is per feed, so the second case checks that a cache
// covering only one feed does not cost the others their cheap 304s.
func TestRefreshFollowing_MissingCacheRefetchesWithoutValidators(t *testing.T) {
	cases := []struct {
		name string
		// seedCached returns the following.json to write before the
		// refresh; nil means the file is never created at all.
		seedCached func(feedA, feedB string) []fetch.Story
		wantCond   map[string]bool // feed → sent a conditional header
		wantURLs   []string        // following.json after the refresh
	}{
		{
			name:       "cache absent: both feeds rebuild from a full 200",
			seedCached: nil,
			wantCond:   map[string]bool{"a": false, "b": false},
			wantURLs:   []string{"https://a.example.com/rebuilt", "https://b.example.com/rebuilt"},
		},
		{
			name: "cache covers feed A only: A keeps its 304, B rebuilds",
			seedCached: func(feedA, _ string) []fetch.Story {
				return []fetch.Story{storyFor(feedA, "https://a.example.com/old")}
			},
			wantCond: map[string]bool{"a": true, "b": false},
			wantURLs: []string{"https://a.example.com/old", "https://b.example.com/rebuilt"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateXDG(t)
			now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

			var mu sync.Mutex
			cond := map[string]bool{}
			// Every handler answers 304 to ANY conditional request and 200
			// otherwise, so a validator that leaks through shows up as a
			// missing story rather than passing by luck. FetchFeeds fans
			// out concurrently, hence the mutex.
			handler := func(name, link string) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					conditional := r.Header.Get("If-None-Match") != "" ||
						r.Header.Get("If-Modified-Since") != ""
					mu.Lock()
					cond[name] = conditional
					mu.Unlock()
					if conditional {
						w.WriteHeader(http.StatusNotModified)
						return
					}
					w.Header().Set("ETag", `"`+name+`-v2"`)
					w.Header().Set("Content-Type", "application/xml")
					fmt.Fprint(w, rssDoc(rssEntry(name+" rebuilt", link, "2026-09-01T10:00:00Z")))
				}
			}
			mux := http.NewServeMux()
			mux.HandleFunc("/a", handler("a", "https://a.example.com/rebuilt"))
			mux.HandleFunc("/b", handler("b", "https://b.example.com/rebuilt"))
			srv := httptest.NewServer(mux)
			defer srv.Close()
			feedA, feedB := srv.URL+"/a", srv.URL+"/b"

			// feeds.json keeps its validators in both cases: that is the
			// state a piped --uninstall leaves behind.
			seedFeedState(t, []string{feedA, feedB}, []feedstate.Observation{
				{URL: feedA, PubDates: []time.Time{now.Add(-24 * time.Hour)}, Items: 1, DatesKnown: true, ETag: `"a-v1"`},
				{URL: feedB, PubDates: []time.Time{now.Add(-24 * time.Hour)}, Items: 1, DatesKnown: true, ETag: `"b-v1"`, LastModified: "Tue, 01 Sep 2026 10:00:00 GMT"},
			}, now.Add(-time.Hour))

			cachePath, err := cache.PoolPath("following")
			if err != nil {
				t.Fatalf("cache.PoolPath: %v", err)
			}
			if tc.seedCached == nil {
				// The absent-cache case proves nothing if the file is
				// there, so fail loudly rather than silently degrade.
				if _, err := os.Stat(cachePath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("following.json exists before the refresh (stat err = %v), want it absent", err)
				}
			} else {
				seedFollowingCache(t, tc.seedCached(feedA, feedB), now.Add(-2*time.Hour))
			}

			cfg := config.Defaults()
			cfg.Pools = []string{"news", "following"}
			cfg.Following = config.FollowingConfig{Feeds: []config.FeedConfig{{URL: feedA}, {URL: feedB}}}

			if err := refreshFollowing(context.Background(), cfg, now); err != nil {
				t.Fatalf("refreshFollowing: %v", err)
			}

			mu.Lock()
			sent := map[string]bool{"a": cond["a"], "b": cond["b"]}
			mu.Unlock()
			for _, name := range []string{"a", "b"} {
				if sent[name] != tc.wantCond[name] {
					t.Errorf("feed %s received conditional-GET headers = %v, want %v — validators must be suppressed for exactly the feeds the cache no longer covers", name, sent[name], tc.wantCond[name])
				}
			}

			f, err := cache.Read(cachePath)
			if err != nil {
				t.Fatalf("read following.json: %v", err)
			}
			if got := storyURLs(f.Stories); !slices.Equal(got, tc.wantURLs) {
				t.Errorf("following.json stories = %v, want %v", got, tc.wantURLs)
			}
		})
	}
}

// TestRefreshFollowing_AllNotModifiedAdvancesFetchedAt pins ruling R-23.
// "Cache content untouched" is about stories, not the timestamp: if
// FetchedAt stayed pinned, IsFresh would be false forever and every
// terminal open would spawn a refresh that changes nothing.
func TestRefreshFollowing_AllNotModifiedAdvancesFetchedAt(t *testing.T) {
	isolateXDG(t)

	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, rssDoc(rssEntry("REFETCHED", "https://feed.example.com/refetched", "2026-09-01T09:00:00Z")))
	}))
	defer srv.Close()

	feed := srv.URL + "/feed.xml"
	seedFeedState(t, []string{feed}, []feedstate.Observation{
		{URL: feed, PubDates: []time.Time{now.Add(-24 * time.Hour)}, Items: 1, DatesKnown: true, ETag: `"v1"`},
	}, now.Add(-time.Hour))
	stale := now.Add(-2 * time.Hour)
	cachePath := seedFollowingCache(t, []fetch.Story{storyFor(feed, "https://feed.example.com/kept")}, stale)

	cfg := config.Defaults()
	cfg.Pools = []string{"news", "following"}
	cfg.Following = config.FollowingConfig{Feeds: []config.FeedConfig{{URL: feed}}}

	if err := refreshFollowing(context.Background(), cfg, now); err != nil {
		t.Fatalf("refreshFollowing: %v", err)
	}

	f, err := cache.Read(cachePath)
	if err != nil {
		t.Fatalf("read following.json: %v", err)
	}
	if got := storyURLs(f.Stories); !slices.Equal(got, []string{"https://feed.example.com/kept"}) {
		t.Errorf("stories = %v, want the cached story preserved across a 304", got)
	}
	if !f.FetchedAt.Equal(now) {
		t.Errorf("FetchedAt = %s, want it advanced to %s on an all-304 refresh", f.FetchedAt, now)
	}
}

// TestRefreshFollowing_AllFeedsFailedWritesNothing is the other half of
// R-23: when every feed errored there is no new truth, so the stale file
// and its old FetchedAt must both stand. The pool reports the failure to
// its caller, which is what lets runRefresh tell "one pool down" from
// "every pool down".
func TestRefreshFollowing_AllFeedsFailedWritesNothing(t *testing.T) {
	isolateXDG(t)

	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	feed := srv.URL + "/feed.xml"
	stale := now.Add(-3 * time.Hour)
	cachePath := seedFollowingCache(t, []fetch.Story{storyFor(feed, "https://feed.example.com/old")}, stale)
	before, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("read seeded cache: %v", err)
	}

	cfg := config.Defaults()
	cfg.Pools = []string{"news", "following"}
	cfg.Following = config.FollowingConfig{Feeds: []config.FeedConfig{{URL: feed}}}

	if err := refreshFollowing(context.Background(), cfg, now); err == nil {
		t.Fatal("refreshFollowing() = nil with every feed failing, want an error so runRefresh can count the pool as failed")
	}

	after, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("read cache after refresh: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("following.json was rewritten on a total failure:\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestRefreshFollowing_SkipsWithoutTouchingFeedState covers the two guards
// whose failure mode is silent and expensive: feedstate.Update GCs every
// feed not in its configured list, so one refresh that reached Update with
// an empty list would wipe four weeks of cadence history and every stored
// validator. The pool must be skipped whole instead.
func TestRefreshFollowing_SkipsWithoutTouchingFeedState(t *testing.T) {
	const feed = "https://feed.example.com/feed.xml"
	cases := []struct {
		name string
		cfg  func(config.Config) config.Config
	}{
		{
			name: "pool disabled but feeds still configured",
			cfg: func(c config.Config) config.Config {
				c.Pools = []string{"news"}
				c.Following = config.FollowingConfig{Feeds: []config.FeedConfig{{URL: feed}}}
				return c
			},
		},
		{
			name: "pool enabled with no feeds",
			cfg: func(c config.Config) config.Config {
				c.Pools = []string{"news", "following"}
				c.Following = config.FollowingConfig{}
				return c
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateXDG(t)
			now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
			fsPath := seedFeedState(t, []string{feed}, []feedstate.Observation{
				{URL: feed, PubDates: []time.Time{now.Add(-24 * time.Hour)}, Items: 1, DatesKnown: true, ETag: `"v1"`},
			}, now.Add(-time.Hour))
			before, err := os.ReadFile(fsPath)
			if err != nil {
				t.Fatalf("read seeded feeds.json: %v", err)
			}

			if err := refreshFollowing(context.Background(), tc.cfg(config.Defaults()), now); err != nil {
				t.Fatalf("refreshFollowing() = %v, want nil for a skipped pool", err)
			}

			after, err := os.ReadFile(fsPath)
			if err != nil {
				t.Fatalf("read feeds.json after refresh: %v", err)
			}
			if !bytes.Equal(before, after) {
				t.Errorf("feeds.json changed while the following pool was skipped:\nbefore: %s\nafter:  %s", before, after)
			}
			cachePath, err := cache.PoolPath("following")
			if err != nil {
				t.Fatalf("cache.PoolPath: %v", err)
			}
			if _, err := os.Stat(cachePath); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("following.json exists after a skipped refresh (stat err = %v); an empty write would erase a good cache", err)
			}
		})
	}
}

// TestRefreshFollowing_EmptyFanOutIsNothingToDo covers FetchFeeds'
// (nil, nil, nil) return for zero feeds. It must read as "nothing to do",
// never as "the feed set is now empty": writing that result would blank a
// good following.json, and calling feedstate.Update with it would still GC
// against the configured list. The seam makes the branch reachable from a
// test without a config that production validation would reject.
func TestRefreshFollowing_EmptyFanOutIsNothingToDo(t *testing.T) {
	isolateXDG(t)

	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	const feed = "https://feed.example.com/feed.xml"
	stale := now.Add(-4 * time.Hour)
	cachePath := seedFollowingCache(t, []fetch.Story{storyFor(feed, "https://feed.example.com/old")}, stale)
	before, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("read seeded cache: %v", err)
	}

	original := newFollowing
	newFollowing = func(specs []fetch.FeedSpec, validators func(string) (string, string)) *fetch.Following {
		return &fetch.Following{} // no feeds → FetchFeeds returns (nil, nil, nil)
	}
	t.Cleanup(func() { newFollowing = original })

	cfg := config.Defaults()
	cfg.Pools = []string{"news", "following"}
	cfg.Following = config.FollowingConfig{Feeds: []config.FeedConfig{{URL: feed}}}

	if err := refreshFollowing(context.Background(), cfg, now); err != nil {
		t.Fatalf("refreshFollowing() = %v, want nil for an empty fan-out", err)
	}
	after, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("read cache after refresh: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("following.json was rewritten from an empty fan-out:\nbefore: %s\nafter:  %s", before, after)
	}
}
