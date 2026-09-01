package fetch_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/fetch"
)

// rssFeedXML assembles a minimal RSS 2.0 document from pre-built <item>
// blocks, mirroring feedparse_test.go's fixture style (Task 2 lives in
// package fetch and its fixtures aren't visible here).
func rssFeedXML(items ...string) string {
	return `<rss version="2.0"><channel><title>Test feed</title>` +
		strings.Join(items, "") + `</channel></rss>`
}

// rssItemXML builds one <item>. pubDate == "" omits the element entirely,
// producing an undated item (HasDate=false).
func rssItemXML(title, link, desc, pubDate string) string {
	pd := ""
	if pubDate != "" {
		pd = fmt.Sprintf("<pubDate>%s</pubDate>", pubDate)
	}
	return fmt.Sprintf(
		"<item><title>%s</title><link>%s</link><description>%s</description>%s<category>news</category></item>",
		title, link, desc, pd,
	)
}

// atomFeedXML assembles a minimal Atom document with a single dated entry.
func atomFeedXML(title, link, summary, published string) string {
	return fmt.Sprintf(`<feed xmlns="http://www.w3.org/2005/Atom"><title>Test atom feed</title>
<entry><title>%s</title><link rel="alternate" href="%s"/><summary>%s</summary><published>%s</published><category term="atomtag"/></entry>
</feed>`, title, link, summary, published)
}

func newXMLServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func storyByURL(stories []fetch.Story, url string) *fetch.Story {
	for i := range stories {
		if stories[i].URL == url {
			return &stories[i]
		}
	}
	return nil
}

func TestFollowing_FetchFeeds_FanOut(t *testing.T) {
	// Fixed literal dates, chosen in the far future so they sort ahead of
	// the undated item's fetch-time timestamp regardless of when the test
	// runs (FetchFeeds does no window math against these, so "far future"
	// is just a stable ordering trick, not a clock read).
	oldest := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	middle := time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC)
	newest := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	layout := time.RFC1123Z

	rssBody := rssFeedXML(
		rssItemXML("RSS oldest", "https://example.com/rss/oldest", "Summary about golang.", oldest.Format(layout)),
		rssItemXML("RSS middle", "https://example.com/rss/middle", "Summary about testing.", middle.Format(layout)),
		rssItemXML("RSS newest", "https://example.com/rss/newest", "Summary about release notes.", newest.Format(layout)),
		rssItemXML("RSS undated", "https://example.com/rss/undated", "Summary with no pubDate.", ""),
	)
	rssSrv := newXMLServer(t, rssBody)

	atomDate := time.Date(2029, 3, 1, 0, 0, 0, 0, time.UTC)
	atomBody := atomFeedXML("Atom entry", "https://example.com/atom/one", "Summary about atom.", atomDate.Format(time.RFC3339))
	atomSrv := newXMLServer(t, atomBody)

	f := &fetch.Following{
		Feeds: []fetch.FeedSpec{
			{URL: rssSrv.URL, MaxItems: 2},
			{URL: atomSrv.URL},
		},
	}

	stories, results, errs := f.FetchFeeds(context.Background())
	if errs != nil {
		t.Fatalf("errs = %v, want nil", errs)
	}
	if len(stories) != 3 {
		t.Fatalf("len(stories) = %d, want 3 (2 kept from RSS + 1 from Atom)", len(stories))
	}

	for _, s := range stories {
		if s.Source != "following" {
			t.Errorf("story %q Source = %q, want %q", s.Title, s.Source, "following")
		}
		if s.Summary == "" {
			t.Errorf("story %q has empty Summary", s.Title)
		}
		if s.Tags == nil {
			t.Errorf("story %q has nil Tags, want non-nil", s.Title)
		}
		var wantFeed string
		switch {
		case strings.HasPrefix(s.URL, "https://example.com/rss/"):
			wantFeed = rssSrv.URL
		case strings.HasPrefix(s.URL, "https://example.com/atom/"):
			wantFeed = atomSrv.URL
		}
		if s.Feed != wantFeed {
			t.Errorf("story %q Feed = %q, want %q (its stub's URL)", s.Title, s.Feed, wantFeed)
		}
	}

	if got := storyByURL(stories, "https://example.com/rss/newest"); got == nil {
		t.Error("RSS newest item missing from kept stories")
	} else if got.Feed != rssSrv.URL {
		t.Errorf("RSS newest Feed = %q, want %q", got.Feed, rssSrv.URL)
	}
	if got := storyByURL(stories, "https://example.com/rss/middle"); got == nil {
		t.Error("RSS middle item missing from kept stories")
	}
	if got := storyByURL(stories, "https://example.com/rss/oldest"); got != nil {
		t.Error("RSS oldest item should have been dropped by MaxItems=2")
	}
	if got := storyByURL(stories, "https://example.com/rss/undated"); got != nil {
		t.Error("RSS undated item should have been dropped by MaxItems=2")
	}
	if got := storyByURL(stories, "https://example.com/atom/one"); got == nil {
		t.Error("Atom item missing from kept stories")
	} else if got.Feed != atomSrv.URL {
		t.Errorf("Atom item Feed = %q, want %q", got.Feed, atomSrv.URL)
	}

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	var rssRes *fetch.FeedResult
	for i := range results {
		if results[i].URL == rssSrv.URL {
			rssRes = &results[i]
		}
		if results[i].NotModified {
			t.Errorf("result %q NotModified = true, want false", results[i].URL)
		}
	}
	if rssRes == nil {
		t.Fatal("no FeedResult for the RSS feed")
	}
	if len(rssRes.ItemDates) != 3 {
		t.Fatalf("RSS ItemDates = %v, want exactly 3 (all dated items, uncapped by MaxItems)", rssRes.ItemDates)
	}
	// Items counts the same scope as ItemDates — every item that survived
	// to Story mapping, before the MaxItems cap — including the undated
	// one. feedstate needs the total to tell "this document dated nothing"
	// from "this document held nothing".
	if rssRes.Items != 4 {
		t.Errorf("RSS Items = %d, want 4 (3 dated + 1 undated, uncapped by MaxItems=2)", rssRes.Items)
	}
	// Count occurrences rather than just membership: a bug that emitted
	// [newest, newest, newest] would still pass a pure membership check
	// against len==3, so pin each expected date to exactly one occurrence.
	seen := make(map[int64]int, 3)
	for _, d := range rssRes.ItemDates {
		seen[d.Unix()]++
	}
	for _, want := range []time.Time{oldest, middle, newest} {
		if seen[want.Unix()] != 1 {
			t.Errorf("ItemDates contains %v %d time(s), want exactly 1 (ItemDates = %v)", want, seen[want.Unix()], rssRes.ItemDates)
		}
	}
}

func TestFollowing_ConditionalGET(t *testing.T) {
	const sentETag = `"tag1"`
	const sentLM = "Mon, 02 Jan 2006 15:04:05 GMT"

	var (
		hdrMu  sync.Mutex
		gotINM string
		gotIMS string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdrMu.Lock()
		gotINM = r.Header.Get("If-None-Match")
		gotIMS = r.Header.Get("If-Modified-Since")
		hdrMu.Unlock()
		w.Header().Set("ETag", `"tag2"`)
		w.WriteHeader(http.StatusNotModified)
	}))
	t.Cleanup(srv.Close)

	f := &fetch.Following{
		Feeds: []fetch.FeedSpec{{URL: srv.URL}},
		Validators: func(url string) (etag, lm string) {
			if url != srv.URL {
				t.Errorf("Validators called with url = %q, want %q", url, srv.URL)
			}
			return sentETag, sentLM
		},
	}

	stories, results, errs := f.FetchFeeds(context.Background())
	if errs != nil {
		t.Fatalf("errs = %v, want nil", errs)
	}
	if len(stories) != 0 {
		t.Errorf("stories = %v, want none for a 304", stories)
	}
	hdrMu.Lock()
	gotINMSnapshot, gotIMSSnapshot := gotINM, gotIMS
	hdrMu.Unlock()
	if gotINMSnapshot != sentETag {
		t.Errorf("server saw If-None-Match = %q, want %q", gotINMSnapshot, sentETag)
	}
	if gotIMSSnapshot != sentLM {
		t.Errorf("server saw If-Modified-Since = %q, want %q", gotIMSSnapshot, sentLM)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	res := results[0]
	if !res.NotModified {
		t.Error("NotModified = false, want true on a 304")
	}
	if res.ItemDates != nil {
		t.Errorf("ItemDates = %v, want nil on a 304", res.ItemDates)
	}
	if res.Items != 0 {
		t.Errorf("Items = %d, want 0 on a 304 (no document was fetched)", res.Items)
	}
	if res.ETag != `"tag2"` {
		t.Errorf("ETag = %q, want the server's refreshed %q", res.ETag, `"tag2"`)
	}
	if res.LastModified != sentLM {
		t.Errorf("LastModified = %q, want it to fall back to the sent value %q (server did not resend one)", res.LastModified, sentLM)
	}
}

func TestFollowing_PartialFailure(t *testing.T) {
	goodBody := rssFeedXML(rssItemXML("Good item", "https://example.com/good", "A fine summary.", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC1123Z)))
	goodSrv := newXMLServer(t, goodBody)

	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(badSrv.Close)

	deadSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := deadSrv.URL
	deadSrv.Close() // connection refused for the fetch below

	f := &fetch.Following{
		Feeds: []fetch.FeedSpec{
			{URL: goodSrv.URL},
			{URL: badSrv.URL},
			{URL: deadURL},
		},
	}

	stories, _, errs := f.FetchFeeds(context.Background())
	if got := storyByURL(stories, "https://example.com/good"); got == nil {
		t.Errorf("good feed's story missing; stories = %v", stories)
	}
	if len(errs) != 2 {
		t.Fatalf("errs = %v, want exactly 2 entries", errs)
	}
	badErr, ok := errs[badSrv.URL]
	if !ok {
		t.Errorf("errs missing entry for the 500 feed %q", badSrv.URL)
	} else if !strings.Contains(badErr.Error(), "status 500") {
		t.Errorf("500 feed error = %q, want it to contain %q", badErr.Error(), "status 500")
	}
	if _, ok := errs[deadURL]; !ok {
		t.Errorf("errs missing entry for the connection-refused feed %q", deadURL)
	}
	if _, ok := errs[goodSrv.URL]; ok {
		t.Errorf("errs should not contain the good feed %q", goodSrv.URL)
	}
}

func TestFollowing_SlowFeedTimesOutAlone(t *testing.T) {
	const slowSleep = 200 * time.Millisecond
	const perFeedTimeout = 50 * time.Millisecond

	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(slowSleep)
		fmt.Fprint(w, rssFeedXML(rssItemXML("Too slow", "https://example.com/slow", "n/a", "")))
	}))
	t.Cleanup(slowSrv.Close)

	fastBody := rssFeedXML(rssItemXML("Fast item", "https://example.com/fast", "Quick summary.", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC1123Z)))
	fastSrv := newXMLServer(t, fastBody)

	f := &fetch.Following{
		Feeds: []fetch.FeedSpec{
			{URL: slowSrv.URL},
			{URL: fastSrv.URL},
		},
		PerFeedTimeout: perFeedTimeout,
	}

	start := time.Now()
	stories, _, errs := f.FetchFeeds(context.Background())
	elapsed := time.Since(start)

	// Bounded comfortably above the 50ms sub-timeout (so this isn't itself
	// flaky under load) but well below the slow handler's 200ms sleep —
	// proves the call actually returned early rather than blocking for the
	// slow feed, i.e. per-feed isolation, not merely "returns eventually".
	const upperBound = 150 * time.Millisecond
	if elapsed >= upperBound {
		t.Errorf("FetchFeeds took %v, want under %v (well below the slow handler's %v sleep, proving isolation)", elapsed, upperBound, slowSleep)
	}
	if got := storyByURL(stories, "https://example.com/fast"); got == nil {
		t.Errorf("fast feed's story missing; stories = %v", stories)
	}
	slowErr, ok := errs[slowSrv.URL]
	if !ok {
		t.Fatalf("errs missing entry for the slow feed %q; errs = %v", slowSrv.URL, errs)
	}
	// errors.Is against context.DeadlineExceeded proves the per-feed
	// sub-timeout actually fired, rather than some other transport failure
	// that happened to also produce a non-nil error.
	if !errors.Is(slowErr, context.DeadlineExceeded) {
		t.Errorf("slow feed error = %v, want it to unwrap to context.DeadlineExceeded", slowErr)
	}
}

func TestFollowing_BodyCapAndUndated(t *testing.T) {
	// testMaxFeedBody mirrors the unexported fetch.maxFeedBody (5<<20);
	// package fetch_test cannot see the production constant, so this is
	// a deliberately duplicated literal — keep it in sync if that value
	// ever changes.
	const testMaxFeedBody = 5 << 20

	t.Run("body over cap errors", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			chunk := bytes.Repeat([]byte("x"), 64<<10)
			remaining := testMaxFeedBody + 1
			for remaining > 0 {
				n := len(chunk)
				if n > remaining {
					n = remaining
				}
				w.Write(chunk[:n])
				remaining -= n
			}
		}))
		t.Cleanup(srv.Close)

		f := &fetch.Following{Feeds: []fetch.FeedSpec{{URL: srv.URL}}}
		stories, results, errs := f.FetchFeeds(context.Background())
		if len(stories) != 0 {
			t.Errorf("stories = %v, want none", stories)
		}
		if len(results) != 0 {
			t.Errorf("results = %v, want none", results)
		}
		bodyErr := errs[srv.URL]
		if bodyErr == nil {
			t.Fatalf("errs = %v, want an entry for the oversized feed", errs)
		}
		// Assert on the guard's own message, not just "some error" — junk
		// bytes also fail XML parsing, so a membership-only check here
		// would pass even if the maxFeedBody guard were deleted entirely.
		if !strings.Contains(bodyErr.Error(), "body exceeds") {
			t.Errorf("oversized-feed error = %q, want it to contain %q (the maxFeedBody guard, not a parse failure)", bodyErr.Error(), "body exceeds")
		}
	})

	t.Run("undated item gets fetch time", func(t *testing.T) {
		body := rssFeedXML(rssItemXML("No date here", "https://example.com/undated-only", "No pubDate at all.", ""))
		srv := newXMLServer(t, body)

		f := &fetch.Following{Feeds: []fetch.FeedSpec{{URL: srv.URL}}}
		before := time.Now()
		stories, _, errs := f.FetchFeeds(context.Background())
		after := time.Now()
		if errs != nil {
			t.Fatalf("errs = %v, want nil", errs)
		}
		if len(stories) != 1 {
			t.Fatalf("len(stories) = %d, want 1", len(stories))
		}
		got := stories[0].CreatedAt
		lo := before.Add(-30 * time.Second)
		hi := after.Add(30 * time.Second)
		if got.Before(lo) || got.After(hi) {
			t.Errorf("CreatedAt = %v, want within 30s of fetch time [%v, %v]", got, lo, hi)
		}
	})
}

func TestFollowing_NegativeMaxItemsDefaults(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	items := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		d := base.AddDate(0, 0, i)
		items = append(items, rssItemXML(
			fmt.Sprintf("Item %d", i),
			fmt.Sprintf("https://example.com/neg/%d", i),
			"Summary text.",
			d.Format(time.RFC1123Z),
		))
	}
	srv := newXMLServer(t, rssFeedXML(items...))

	f := &fetch.Following{Feeds: []fetch.FeedSpec{{URL: srv.URL, MaxItems: -1}}}
	stories, _, errs := f.FetchFeeds(context.Background())
	if errs != nil {
		t.Fatalf("errs = %v, want nil", errs)
	}
	if len(stories) != 3 {
		t.Errorf("len(stories) = %d, want 3 (negative MaxItems falls back to the default)", len(stories))
	}
}

func TestFollowing_ItemLinksResolvedAgainstFeedURL(t *testing.T) {
	// Relative hrefs are legal Atom (RFC 4287, resolved against the feed
	// URL) and appear in RSS too. Unresolved they render as dead links and
	// blank Story.NormalisedHost, which silently disables the diversity
	// host penalty. Non-http(s) schemes have no business in a rendered
	// link at all.
	body := rssFeedXML(
		rssItemXML("Relative item", "/relative/post", "A relative link.", ""),
		rssItemXML("Hostile item", "javascript:alert(1)", "A hostile scheme.", ""),
		rssItemXML("Absolute item", "https://example.com/absolute", "An absolute link.", ""),
	)
	srv := newXMLServer(t, body)

	f := &fetch.Following{Feeds: []fetch.FeedSpec{{URL: srv.URL, MaxItems: 10}}}
	stories, results, errs := f.FetchFeeds(context.Background())
	if errs != nil {
		t.Fatalf("errs = %v, want nil", errs)
	}
	if len(stories) != 2 {
		t.Fatalf("len(stories) = %d, want 2 (the javascript: item dropped); got %v", len(stories), stories)
	}
	if got := storyByURL(stories, srv.URL+"/relative/post"); got == nil {
		t.Errorf("no story at %q; the relative link must resolve against the feed URL. stories = %v", srv.URL+"/relative/post", stories)
	}
	if got := storyByURL(stories, "https://example.com/absolute"); got == nil {
		t.Error("the absolute link must survive unchanged")
	}
	for _, s := range stories {
		if strings.HasPrefix(s.URL, "javascript:") {
			t.Errorf("story %q kept a javascript: URL (%q)", s.Title, s.URL)
		}
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	// An item the URL guard dropped is not an item, so it must not inflate
	// the count feedstate reads as "this document carried items".
	if results[0].Items != 2 {
		t.Errorf("Items = %d, want 2 (the javascript: item is not an item)", results[0].Items)
	}
}

func TestFollowing_ItemLinksResolveAgainstTheRedirectedURL(t *testing.T) {
	// The client follows redirects, so the document that arrives is the one
	// at the FINAL URL, and its relative links are relative to THAT. The
	// redirect crosses into a different directory, which is what separates
	// the two bases: against the configured /start, "post" resolves to
	// /post; against the served /blog/feed.xml it resolves to /blog/post.
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/blog/feed.xml", http.StatusFound)
	})
	mux.HandleFunc("/blog/feed.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, rssFeedXML(rssItemXML("Relative item", "post", "A relative link.", "")))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	configured := srv.URL + "/start"

	f := &fetch.Following{Feeds: []fetch.FeedSpec{{URL: configured, MaxItems: 10}}}
	stories, results, errs := f.FetchFeeds(context.Background())
	if errs != nil {
		t.Fatalf("errs = %v, want nil", errs)
	}
	if len(stories) != 1 {
		t.Fatalf("len(stories) = %d, want 1: %v", len(stories), stories)
	}
	if want := srv.URL + "/blog/post"; stories[0].URL != want {
		t.Errorf("story URL = %q, want %q (resolved against the post-redirect URL, not the configured one)", stories[0].URL, want)
	}
	// Only the resolution base moves. Identity stays the configured URL:
	// that is what the config lists and what feedstate is keyed by.
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].URL != configured {
		t.Errorf("FeedResult.URL = %q, want the configured %q", results[0].URL, configured)
	}
	if stories[0].Feed != configured {
		t.Errorf("Story.Feed = %q, want the configured %q", stories[0].Feed, configured)
	}
}

func TestFollowing_ItemURLGuard(t *testing.T) {
	// Every link reaching Story.URL must be an absolute http(s) URL with a
	// host. Checking the scheme alone is not enough: "http:post" and
	// "https:///post" both carry an accepted scheme and no host at all,
	// and become dead links whose NormalisedHost is empty — which silently
	// disables the diversity host penalty.
	cases := []struct {
		name, link string
		want       string // "" means the item must be dropped
	}{
		{"opaque http", "http:post", ""},
		{"opaque https", "https:post", ""},
		{"scheme and path but no authority", "http:/post", ""},
		{"empty authority", "https:///post", ""},
		{"port with no host", "https://:8080/x", ""},
		{"non-http scheme", "javascript:alert(1)", ""},
		{"root-relative", "/relative/post", "%s/relative/post"},
		{"document-relative", "post.html", "%s/feed/post.html"},
		{"protocol-relative", "//other.example/post", "http://other.example/post"},
		{"already absolute", "https://example.com/post", "https://example.com/post"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Served from a subdirectory so a document-relative link has a
			// directory to be relative to.
			mux := http.NewServeMux()
			mux.HandleFunc("/feed/rss.xml", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				fmt.Fprint(w, rssFeedXML(rssItemXML("Item", tc.link, "A summary.", "")))
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			f := &fetch.Following{Feeds: []fetch.FeedSpec{{URL: srv.URL + "/feed/rss.xml", MaxItems: 10}}}
			stories, results, errs := f.FetchFeeds(context.Background())
			if errs != nil {
				t.Fatalf("errs = %v, want nil", errs)
			}
			want := tc.want
			if strings.Contains(want, "%s") {
				want = fmt.Sprintf(want, srv.URL)
			}
			if want == "" {
				if len(stories) != 0 {
					t.Fatalf("link %q produced %v, want the item dropped", tc.link, stories)
				}
				if results[0].Items != 0 {
					t.Errorf("Items = %d, want 0 (a dropped link is not an item)", results[0].Items)
				}
				return
			}
			if len(stories) != 1 {
				t.Fatalf("link %q produced %d stories, want 1", tc.link, len(stories))
			}
			if stories[0].URL != want {
				t.Errorf("link %q resolved to %q, want %q", tc.link, stories[0].URL, want)
			}
		})
	}
}

func TestFollowing_UserAgent(t *testing.T) {
	var (
		uaMu  sync.Mutex
		gotUA string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uaMu.Lock()
		gotUA = r.Header.Get("User-Agent")
		uaMu.Unlock()
		fmt.Fprint(w, rssFeedXML(rssItemXML("UA check", "https://example.com/ua", "n/a", "")))
	}))
	t.Cleanup(srv.Close)

	f := &fetch.Following{Feeds: []fetch.FeedSpec{{URL: srv.URL}}}
	_, _, errs := f.FetchFeeds(context.Background())
	if errs != nil {
		t.Fatalf("errs = %v, want nil", errs)
	}
	uaMu.Lock()
	uaSnapshot := gotUA
	uaMu.Unlock()
	if !strings.HasPrefix(uaSnapshot, "newsfetch/") {
		t.Errorf("User-Agent = %q, want a %q prefix", uaSnapshot, "newsfetch/")
	}
}

func TestFollowing_UndatedItemSortsAsNewest(t *testing.T) {
	// Locked decision: undated items sort as newest (fetch time). Far-
	// future literal dates elsewhere in this file make the undated item
	// sort LAST (it's older than 2030+ fixtures), so that ordering half of
	// the rule is otherwise never exercised — only CreatedAt's value is
	// checked (see the "undated item gets fetch time" subcase above). Past
	// dated items plus MaxItems: 1 pins the ordering: only the undated
	// item can survive the cap if it truly sorts newest.
	past1 := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	past2 := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)

	body := rssFeedXML(
		rssItemXML("Old item one", "https://example.com/sort/old1", "Old summary one.", past1.Format(time.RFC1123Z)),
		rssItemXML("Old item two", "https://example.com/sort/old2", "Old summary two.", past2.Format(time.RFC1123Z)),
		rssItemXML("Undated item", "https://example.com/sort/undated", "No pubDate at all.", ""),
	)
	srv := newXMLServer(t, body)

	f := &fetch.Following{Feeds: []fetch.FeedSpec{{URL: srv.URL, MaxItems: 1}}}
	stories, _, errs := f.FetchFeeds(context.Background())
	if errs != nil {
		t.Fatalf("errs = %v, want nil", errs)
	}
	if len(stories) != 1 {
		t.Fatalf("len(stories) = %d, want 1", len(stories))
	}
	if got := stories[0].URL; got != "https://example.com/sort/undated" {
		t.Errorf("kept story URL = %q, want the undated item (it must sort newest, ahead of both past-dated items)", got)
	}
}
