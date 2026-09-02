package main

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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
