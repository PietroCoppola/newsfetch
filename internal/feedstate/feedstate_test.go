package feedstate_test

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/feedstate"
)

const week = 7 * 24 * time.Hour

// refNow is the fixed instant every case below is written against. A
// function rather than a package-level var: the project bans global
// mutable state in internal packages, and a shared var one test could
// reassign would couple every other test to the order they run in.
func refNow() time.Time { return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) }

func tmp(t *testing.T) string { t.Helper(); return filepath.Join(t.TempDir(), "feeds.json") }

// datesWithin returns n pubDates inside the 4-week window ending at
// refNow(), 1h apart. Spacing is irrelevant to the rate — only the
// in-window count matters — so n dates mean a rate of n/4 items per week.
func datesWithin(n int) []time.Time {
	out := make([]time.Time, n)
	for i := range out {
		out[i] = refNow().Add(-time.Duration(i+1) * time.Hour)
	}
	return out
}

func TestPath_HonoursXDGStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-state")
	got, err := feedstate.Path()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/tmp/xdg-state", "newsfetch", "feeds.json"); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestUpdate_UpsertsGCsAndPrunes(t *testing.T) {
	now := refNow()
	path := tmp(t)
	// a's second date is 5 weeks old: write-prune must drop it.
	if err := feedstate.Update(path, []string{"https://a/feed", "https://b/feed"}, []feedstate.Observation{
		{URL: "https://a/feed", PubDates: []time.Time{now.Add(-time.Hour), now.Add(-5 * week)}, DatesKnown: true, ETag: `"v1"`},
		{URL: "https://b/feed", PubDates: []time.Time{now.Add(-2 * time.Hour)}, DatesKnown: true},
	}, now); err != nil {
		t.Fatal(err)
	}
	// Second update: a is now a 304 (validators only, dates kept), b is
	// dropped from config (GC'd), c is new.
	if err := feedstate.Update(path, []string{"https://a/feed", "https://c/feed"}, []feedstate.Observation{
		{URL: "https://a/feed", DatesKnown: false, ETag: `"v2"`},
		{URL: "https://c/feed", PubDates: []time.Time{now.Add(-3 * time.Hour)}, DatesKnown: true},
	}, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	f, err := feedstate.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Feeds) != 2 {
		t.Fatalf("feeds = %d, want 2 (b GC'd): %+v", len(f.Feeds), f.Feeds)
	}
	etag, _ := f.Validators("https://a/feed")
	if etag != `"v2"` {
		t.Errorf("a's etag = %q, want v2", etag)
	}
	var a feedstate.Feed
	for _, fd := range f.Feeds {
		if fd.URL == "https://a/feed" {
			a = fd
		}
	}
	if len(a.PubDates) != 1 || !a.PubDates[0].Equal(now.Add(-time.Hour)) {
		t.Errorf("a.PubDates = %v, want the one in-window date (304 keeps dates; write-prune drops the 5-week-old one)", a.PubDates)
	}
	if !a.FirstSeen.Equal(now) {
		t.Errorf("FirstSeen must survive updates; got %v", a.FirstSeen)
	}
}

func TestUpdate_ConcurrentWritersRetainAll(t *testing.T) {
	now := refNow()
	path := tmp(t)
	urls := make([]string, 20)
	for i := range urls {
		urls[i] = "https://feed" + strconv.Itoa(i) + "/rss"
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = feedstate.Update(path, urls, []feedstate.Observation{
				{URL: urls[i], PubDates: []time.Time{now.Add(-time.Hour)}, DatesKnown: true},
			}, now)
		}(i)
	}
	wg.Wait()
	f, err := feedstate.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Feeds) != 20 {
		t.Errorf("retained %d of 20 feeds; concurrent Updates lost writes", len(f.Feeds))
	}
}

// datedFeed builds a Feed whose last fetched document carried len(dates)
// items, every one of them dated — the ordinary shape of a feed with a
// working pubDate, and the shape that makes a current rate of 0 mean
// "went quiet" (dormant, boosted) rather than "carries no date at all"
// (no signal, neutral). Feeds that need a different document shape — an
// empty document, or items whose dates don't parse — set the three
// cadence-signal fields directly. The cases that turn on those fields
// are TestWeights_NeverDatedFeedIsNeutralNotDormant and
// TestWeights_OnceDatedFeedStillEarnsDormantBoost.
func datedFeed(url string, firstSeen time.Time, dates []time.Time) feedstate.Feed {
	return feedstate.Feed{
		URL:          url,
		FirstSeen:    firstSeen,
		ObservedAt:   refNow(),
		PubDates:     dates,
		EverDated:    len(dates) > 0,
		LastDocItems: len(dates),
		LastDocDated: len(dates),
	}
}

func TestWeights(t *testing.T) {
	now := refNow()
	old := now.Add(-8 * week) // 8 weeks: full confidence
	expired := []time.Time{now.Add(-5 * week), now.Add(-6 * week), now.Add(-7 * week)}
	f := &feedstate.File{Version: feedstate.SchemaVersion, Feeds: []feedstate.Feed{
		datedFeed("busy", old, datesWithin(16)),  // 4.0/wk
		datedFeed("median", old, datesWithin(4)), // 1.0/wk
		datedFeed("rare", old, datesWithin(1)),   // 0.25/wk
		datedFeed("dormant", old, expired),       // 0/wk: rolled out
		datedFeed("unconfigured", old, datesWithin(9)),
	}}
	configured := []string{"busy", "median", "rare", "dormant", "fresh-no-observation"}
	w := f.Weights(configured, now)
	// rates {4.0, 1.0, 0.25, 0} (dormant zeros participate) →
	// median = (0.25+1.0)/2 = 0.625
	if got := w["busy"]; math.Abs(got-0.625/4.0) > 1e-9 {
		t.Errorf("busy = %v, want median/rate = %v", got, 0.625/4.0)
	}
	if got := w["rare"]; math.Abs(got-0.625/0.25) > 1e-9 {
		t.Errorf("rare = %v, want %v", got, 0.625/0.25)
	}
	// The feed whose rate defines the reference is the one that pins the
	// reference itself: median/1.0 = 0.625 only if the median really is
	// the median. A mean would give (4+1+0.25+0)/4 = 1.3125 here.
	if got := w["median"]; math.Abs(got-0.625) > 1e-9 {
		t.Errorf("median = %v, want 0.625 (median/its own rate)", got)
	}
	if got := w["dormant"]; got != 5.0 {
		t.Errorf("dormant (all dates expired) = %v, want capped 5.0", got)
	}
	if _, ok := w["unconfigured"]; ok {
		t.Error("unconfigured feeds must not appear in weights")
	}
	if got := w["fresh-no-observation"]; got != 1.0 {
		t.Errorf("feed with no observation = %v, want neutral 1.0", got)
	}
}

func TestWeights_WindowBoundaries(t *testing.T) {
	now := refNow()
	old := now.Add(-8 * week)
	cases := []struct {
		name      string
		dates     []time.Time
		wantProbe float64
	}{
		// Counted → rate 0.25, rates {1.0, 0.25}, median 0.625 → 2.5.
		// Excluded → rate 0, rates {1.0, 0}, median 0.5 → dormant 5.0.
		{"date one second inside the window counts", []time.Time{now.Add(-4*week + time.Second)}, 2.5},
		{"date exactly at the window edge is excluded", []time.Time{now.Add(-4 * week)}, 5.0},
		{"future date is excluded from the count", []time.Time{now.Add(time.Hour)}, 5.0},
		{"expired dates only", []time.Time{now.Add(-5 * week), now.Add(-6 * week)}, 5.0},
		{"expired plus one fresh counts the fresh one", []time.Time{now.Add(-5 * week), now.Add(-6 * week), now.Add(-time.Hour)}, 2.5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &feedstate.File{Version: feedstate.SchemaVersion, Feeds: []feedstate.Feed{
				datedFeed("anchor", old, datesWithin(4)), // 1.0/wk
				datedFeed("probe", old, tc.dates),
			}}
			w := f.Weights([]string{"anchor", "probe"}, now)
			if got := w["probe"]; math.Abs(got-tc.wantProbe) > 1e-9 {
				t.Errorf("probe = %v, want %v", got, tc.wantProbe)
			}
		})
	}
}

func TestWeights_ZeroMedianFallsBackToNonzero(t *testing.T) {
	now := refNow()
	old := now.Add(-8 * week)
	f := &feedstate.File{Version: feedstate.SchemaVersion, Feeds: []feedstate.Feed{
		// d1's last document was empty, from a feed that has been dated
		// before: dormant-eligible with nothing left to count.
		{URL: "d1", FirstSeen: old, ObservedAt: now, EverDated: true}, // 0/wk
		datedFeed("d2", old, []time.Time{now.Add(-5 * week)}),         // 0/wk
		datedFeed("busy", old, datesWithin(16)),                       // 4.0/wk
	}}
	w := f.Weights([]string{"d1", "d2", "busy"}, now)
	// rates {0, 0, 4.0} → median 0 → fall back to nonzero median 4.0:
	// dormant feeds keep their boost, busy is 4.0/4.0 = neutral.
	if got := w["d1"]; got != 5.0 {
		t.Errorf("d1 = %v, want 5.0 (zero median must not strip the dormant boost)", got)
	}
	if got := w["d2"]; got != 5.0 {
		t.Errorf("d2 = %v, want 5.0", got)
	}
	if got := w["busy"]; math.Abs(got-1.0) > 1e-9 {
		t.Errorf("busy = %v, want 1.0 (nonzero-median fallback over own rate)", got)
	}
}

func TestWeights_AllDormantNeutral(t *testing.T) {
	now := refNow()
	old := now.Add(-8 * week)
	f := &feedstate.File{Version: feedstate.SchemaVersion, Feeds: []feedstate.Feed{
		{URL: "d1", FirstSeen: old, ObservedAt: now, EverDated: true},
		{URL: "d2", FirstSeen: old, ObservedAt: now, EverDated: true},
	}}
	w := f.Weights([]string{"d1", "d2"}, now)
	// No in-window activity anywhere: no signal. Uniform weights are
	// relative, so neutral 1.0 for everyone.
	if w["d1"] != 1.0 || w["d2"] != 1.0 {
		t.Errorf("all-dormant corpus = %v, want all 1.0", w)
	}
}

func TestWeights_ColdStartBlend(t *testing.T) {
	now := refNow()
	twoWeeks := now.Add(-2 * week) // confidence 0.5
	old := now.Add(-8 * week)
	f := &feedstate.File{Version: feedstate.SchemaVersion, Feeds: []feedstate.Feed{
		datedFeed("new-rare", twoWeeks, datesWithin(1)), // 0.25/wk
		datedFeed("anchor", old, datesWithin(4)),        // 1.0/wk
	}}
	w := f.Weights([]string{"new-rare", "anchor"}, now)
	// median of {0.25, 1.0} = 0.625; computed = 0.625/0.25 = 2.5;
	// confidence 0.5 → 0.5*2.5 + 0.5*1.0 = 1.75
	if got := w["new-rare"]; math.Abs(got-1.75) > 1e-9 {
		t.Errorf("cold-start blend = %v, want 1.75", got)
	}
}

func TestWeights_FutureFirstSeenClampsToNeutral(t *testing.T) {
	now := refNow()
	old := now.Add(-8 * week)
	f := &feedstate.File{Version: feedstate.SchemaVersion, Feeds: []feedstate.Feed{
		datedFeed("skewed", now.Add(week), datesWithin(16)),
		datedFeed("anchor", old, datesWithin(4)),
	}}
	w := f.Weights([]string{"skewed", "anchor"}, now)
	// A future FirstSeen (clock skew) must clamp confidence to 0, never
	// go negative: the feed is fully neutral.
	if got := w["skewed"]; got != 1.0 {
		t.Errorf("future FirstSeen = %v, want 1.0 (confidence clamped to [0,1])", got)
	}
}

func TestWeights_RollsAcrossNotModified(t *testing.T) {
	now := refNow()
	path := tmp(t)
	later := now.Add(6 * week)
	cfg := []string{"https://a/feed", "https://b/feed"}
	// a published once just before now and then went quiet behind 304s;
	// b's items sit just before the later read point (future dates are
	// stored and start counting once the window reaches them).
	if err := feedstate.Update(path, cfg, []feedstate.Observation{
		{URL: "https://a/feed", PubDates: []time.Time{now.Add(-time.Hour)}, Items: 1, DatesKnown: true},
		{URL: "https://b/feed", PubDates: []time.Time{
			later.Add(-1 * time.Hour), later.Add(-2 * time.Hour),
			later.Add(-3 * time.Hour), later.Add(-4 * time.Hour),
		}, Items: 4, DatesKnown: true},
	}, now); err != nil {
		t.Fatal(err)
	}
	// Six weeks of nothing but 304s: validators refresh, dates untouched.
	if err := feedstate.Update(path, cfg, []feedstate.Observation{
		{URL: "https://a/feed", DatesKnown: false},
		{URL: "https://b/feed", DatesKnown: false},
	}, later); err != nil {
		t.Fatal(err)
	}
	f, err := feedstate.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	w := f.Weights(cfg, later)
	// a's only item aged out of the rolling window with no refetch:
	// rate 0 → the dormant boost arrives across 304s. b: 4 in-window
	// dates / 4 weeks = 1.0; rates {0, 1.0} → median 0.5 → b = 0.5.
	if got := w["https://a/feed"]; got != 5.0 {
		t.Errorf("a = %v, want 5.0 (dormancy must arrive across 304s — the window rolls even when the document doesn't)", got)
	}
	if got := w["https://b/feed"]; math.Abs(got-0.5) > 1e-9 {
		t.Errorf("b = %v, want 0.5", got)
	}
}

// TestUpdate_ValidatorsFollowTheObservation pins both directions of the
// rule that the observation is always the truth about validators. The
// caller resolves them before reporting: fetchOne prefers the response's
// own headers and back-fills the ones it sent when a 304 omits them, so
// what arrives here is either newer than the stored pair or equal to it.
func TestUpdate_ValidatorsFollowTheObservation(t *testing.T) {
	now := refNow()
	const url = "https://a/feed"
	const firstLM = "Mon, 24 Aug 2026 10:00:00 GMT"
	const secondLM = "Tue, 25 Aug 2026 10:00:00 GMT"
	cases := []struct {
		name             string
		second           feedstate.Observation
		wantETag, wantLM string
	}{
		{
			// The guard that only overwrote non-empty validators made it
			// impossible to record that a feed STOPPED sending one, pinning
			// a stale validator forever and re-requesting against a
			// validator the server no longer honours.
			name:   "a 200 that stopped sending validators clears both",
			second: feedstate.Observation{URL: url, PubDates: datesWithin(1), DatesKnown: true},
		},
		{
			name: "a 200 replaces both validators",
			second: feedstate.Observation{
				URL: url, PubDates: datesWithin(1), DatesKnown: true,
				ETag: `"v2"`, LastModified: secondLM,
			},
			wantETag: `"v2"`, wantLM: secondLM,
		},
		{
			// RFC 7232 lets a server regenerate the validator on a 304 and
			// expects the client to store the new one. Gating the write on
			// "was this a 200" would throw that refresh away.
			name: "a 304 carrying refreshed validators updates both",
			second: feedstate.Observation{
				URL: url, DatesKnown: false,
				ETag: `"v2"`, LastModified: secondLM,
			},
			wantETag: `"v2"`, wantLM: secondLM,
		},
		{
			// The shape fetchOne produces when a 304 resends neither
			// validator: it back-fills the ones it sent, so the write is a
			// no-op restatement of what is already stored.
			name: "a 304 that resent neither keeps the back-filled pair",
			second: feedstate.Observation{
				URL: url, DatesKnown: false,
				ETag: `"v1"`, LastModified: firstLM,
			},
			wantETag: `"v1"`, wantLM: firstLM,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tmp(t)
			if err := feedstate.Update(path, []string{url}, []feedstate.Observation{
				{URL: url, PubDates: datesWithin(1), DatesKnown: true, ETag: `"v1"`, LastModified: firstLM},
			}, now); err != nil {
				t.Fatal(err)
			}
			if err := feedstate.Update(path, []string{url}, []feedstate.Observation{tc.second}, now.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			f, err := feedstate.Read(path)
			if err != nil {
				t.Fatal(err)
			}
			etag, lm := f.Validators(url)
			if etag != tc.wantETag {
				t.Errorf("ETag = %q, want %q", etag, tc.wantETag)
			}
			if lm != tc.wantLM {
				t.Errorf("LastModified = %q, want %q", lm, tc.wantLM)
			}
		})
	}
}

func TestWeights_NeverDatedFeedIsNeutralNotDormant(t *testing.T) {
	now := refNow()
	// A feed whose dates all fail to parse reports zero pubDates, which was
	// indistinguishable from "published nothing in four weeks" and earned
	// the max dormant boost — while every undated item also takes fetch
	// time as CreatedAt, i.e. maximal recency. 5.0 × max recency forever,
	// on one malformed feed. No signal is not dormancy.
	path := tmp(t)
	early := now.Add(-8 * week) // full confidence by `now`
	cfg := []string{"https://undated/feed", "https://dated/feed"}
	if err := feedstate.Update(path, cfg, []feedstate.Observation{
		{URL: "https://undated/feed", Items: 4, DatesKnown: true}, // 200 with items, not one parseable date
		{URL: "https://dated/feed", PubDates: datesWithin(4), Items: 4, DatesKnown: true},
	}, early); err != nil {
		t.Fatal(err)
	}
	f, err := feedstate.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	w := f.Weights(cfg, now)
	if got := w["https://undated/feed"]; got != 1.0 {
		t.Errorf("never-dated feed = %v, want neutral 1.0 (no cadence signal is not dormancy)", got)
	}
	// The never-dated feed must also stay out of the corpus median, the
	// same as a feed with no observation at all: a rate it never reported
	// is not a zero to average in. Alone in the corpus, dated's own rate
	// is the reference, so it is exactly neutral.
	if got := w["https://dated/feed"]; math.Abs(got-1.0) > 1e-9 {
		t.Errorf("dated feed = %v, want 1.0 (a never-dated feed must not drag the median)", got)
	}
}

// TestWeights_OnceDatedFeedStillEarnsDormantBoost walks the whole
// dormant-eligibility matrix. The 5.0 exists for a feed that showed a
// cadence and then went quiet, and that history has to survive a
// round-trip through feeds.json — but "quiet" has to mean the DOCUMENT
// went quiet. A feed still shipping items whose dates no longer parse is
// not quiet: it has no current cadence signal, and every one of those
// undated items also takes fetch time as its timestamp, so reading it as
// dormancy hands one malformed feed max boost x max recency forever.
// A historical latch alone cannot tell those apart, so every row here
// goes through real Updates and a Read.
func TestWeights_OnceDatedFeedStillEarnsDormantBoost(t *testing.T) {
	now := refNow()
	// Observations are written 8 weeks before the read point, so
	// confidence is 1 and the cold-start blend is a no-op. The anchor's
	// dates are in the future at write time (the write-prune keeps those)
	// and in-window at the read point, pinning its rate at 1.0/wk there.
	writeAt := now.Add(-8 * week)
	const probe, anchor = "https://probe/feed", "https://anchor/feed"
	cfg := []string{probe, anchor}
	// In-window when written, aged out by the read point.
	agedOut := []time.Time{writeAt.Add(-time.Hour), writeAt.Add(-2 * time.Hour)}

	cases := []struct {
		name string
		obs  []feedstate.Observation // the probe's history, one Update apiece
		want float64
	}{
		{
			name: "never dated, items but no dates: no signal at all",
			obs:  []feedstate.Observation{{URL: probe, Items: 3, DatesKnown: true}},
			want: 1.0,
		},
		{
			name: "once dated, still shipping items, none dated: no CURRENT signal",
			obs: []feedstate.Observation{
				{URL: probe, PubDates: agedOut[:1], Items: 1, DatesKnown: true},
				{URL: probe, Items: 3, DatesKnown: true},
			},
			want: 1.0,
		},
		{
			name: "once dated, document now empty: genuinely quiet",
			obs: []feedstate.Observation{
				{URL: probe, PubDates: agedOut[:1], Items: 1, DatesKnown: true},
				{URL: probe, DatesKnown: true},
			},
			want: 5.0,
		},
		{
			name: "dated document whose dates all aged out",
			obs:  []feedstate.Observation{{URL: probe, PubDates: agedOut, Items: 2, DatesKnown: true}},
			want: 5.0,
		},
		{
			name: "never dated, empty document: still no signal",
			obs:  []feedstate.Observation{{URL: probe, DatesKnown: true}},
			want: 1.0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tmp(t)
			for i, o := range tc.obs {
				batch := []feedstate.Observation{o}
				if i == 0 {
					batch = append(batch, feedstate.Observation{
						URL: anchor, PubDates: datesWithin(4), Items: 4, DatesKnown: true,
					})
				}
				if err := feedstate.Update(path, cfg, batch, writeAt.Add(time.Duration(i)*time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			f, err := feedstate.Read(path)
			if err != nil {
				t.Fatal(err)
			}
			w := f.Weights(cfg, now)
			if got := w[probe]; math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("probe = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestWeights_NotModifiedRetainsDocumentShape pins the retention of the
// two LastDoc counts across a 304, which is what lets the eligibility rule
// keep telling a quiet document from an undated one when the fetch brings
// back no document at all.
//
// The sequence has to be exactly this one to discriminate. Take the
// obvious probe — dated, then a 304 — and zeroing the counts on the 304
// would still answer 5.0, because LastDocItems == 0 with EverDated set
// reads as "genuinely quiet". The only sequence where the two outcomes
// differ is a NON-EMPTY undated document behind the 304: it must stay
// neutral, and a regression that dropped the counts would flip it to the
// dormant 5.0 on the strength of the historical latch alone.
func TestWeights_NotModifiedRetainsDocumentShape(t *testing.T) {
	now := refNow()
	path := tmp(t)
	writeAt := now.Add(-8 * week) // full confidence by `now`
	const probe, anchor = "https://probe/feed", "https://anchor/feed"
	cfg := []string{probe, anchor}

	// 200 with a dated item: latches EverDated.
	if err := feedstate.Update(path, cfg, []feedstate.Observation{
		{URL: probe, PubDates: []time.Time{writeAt.Add(-time.Hour)}, Items: 1, DatesKnown: true},
		{URL: anchor, PubDates: datesWithin(4), Items: 4, DatesKnown: true},
	}, writeAt); err != nil {
		t.Fatal(err)
	}
	// 200 still shipping items, none of whose dates parse: no signal.
	if err := feedstate.Update(path, cfg, []feedstate.Observation{
		{URL: probe, Items: 3, DatesKnown: true},
	}, writeAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// 304: no document, so the stored shape is all there is to go on.
	if err := feedstate.Update(path, cfg, []feedstate.Observation{
		{URL: probe, DatesKnown: false},
	}, writeAt.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}

	f, err := feedstate.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, fd := range f.Feeds {
		if fd.URL != probe {
			continue
		}
		if fd.LastDocItems != 3 || fd.LastDocDated != 0 {
			t.Errorf("after the 304: LastDocItems=%d LastDocDated=%d, want 3 and 0 (a 304 keeps the last document's shape)",
				fd.LastDocItems, fd.LastDocDated)
		}
	}
	if got := f.Weights(cfg, now)[probe]; math.Abs(got-1.0) > 1e-9 {
		t.Errorf("probe = %v, want neutral 1.0 (a 304 over an undated document must not read as dormancy)", got)
	}
}

// datesExpired returns n pubDates five weeks before refNow(): dated items
// that have rolled out of the 4-week rate window. A feed holding only
// these has a rate of 0 and is dormant — the state whose boost coverage
// scaling has to discount when the document dated only part of itself.
func datesExpired(n int) []time.Time {
	out := make([]time.Time, n)
	for i := range out {
		out[i] = refNow().Add(-5 * week).Add(-time.Duration(i) * time.Hour)
	}
	return out
}

// TestWeights_CoverageScalesTheCadenceWeight pins the repair of the
// mixed-document gap: the capped cadence weight is multiplied by the last
// full document's date coverage, LastDocDated/LastDocItems.
//
// Left unscaled, a document with one dated item in fifty (that date out of
// window) satisfies the dormant-eligibility rule on the strength of its
// single date and takes the full cap — while its forty-nine undated items
// each take fetch time as their timestamp, i.e. maximum recency. Max boost
// times max recency, on every render, from one badly dated feed.
//
// Every case pairs the probe with an anchor publishing 4 in-window items
// (rate 1.0/wk), so the corpus median is a known number, and gives both
// feeds 8 weeks of history so confidence is 1 and the cold-start blend is
// a no-op. wantAnchor is asserted too: a probe with no cadence signal
// drops out of the median and moves the anchor's own weight, and that is
// behaviour a coverage change must not quietly alter.
func TestWeights_CoverageScalesTheCadenceWeight(t *testing.T) {
	now := refNow()
	old := now.Add(-8 * week)

	cases := []struct {
		name       string
		dates      []time.Time
		items      int
		dated      int
		wantProbe  float64
		wantAnchor float64
	}{
		// rates {probe 0, anchor 1.0} -> median (0+1.0)/2 = 0.5.
		// probe: rate 0 -> computed 5.0; coverage 50/50 = 1.0 -> 5.0.
		// anchor: computed 0.5/1.0 = 0.5; coverage 4/4 = 1.0 -> 0.5.
		{
			name:       "every item dated keeps the whole dormant boost",
			dates:      datesExpired(50),
			items:      50,
			dated:      50,
			wantProbe:  5.0,
			wantAnchor: 0.5,
		},
		// rates {0, 1.0} -> median 0.5. probe: 5.0 * (25/50 = 0.5) = 2.5.
		// anchor: 0.5/1.0 = 0.5 * 1.0 = 0.5.
		{
			name:       "half the document dated halves the boost",
			dates:      datesExpired(25),
			items:      50,
			dated:      25,
			wantProbe:  2.5,
			wantAnchor: 0.5,
		},
		// rates {0, 1.0} -> median 0.5. probe: 5.0 * (1/50 = 0.02) = 0.1,
		// i.e. effectively out of the running rather than at the cap.
		// anchor: 0.5/1.0 = 0.5 * 1.0 = 0.5.
		{
			name:       "one dated item in fifty is effectively neutralised",
			dates:      datesExpired(1),
			items:      50,
			dated:      1,
			wantProbe:  0.1,
			wantAnchor: 0.5,
		},
		// dated 0 with items present is not dormant-eligible at all, so the
		// probe is neutral 1.0 and contributes no rate to the corpus:
		// rates {1.0} -> median 1.0 -> anchor 1.0/1.0 = 1.0. Coverage never
		// gets a say here; the eligibility rule already handled it.
		{
			name:       "no dated items at all stays neutral, not scaled",
			dates:      nil,
			items:      50,
			dated:      0,
			wantProbe:  1.0,
			wantAnchor: 1.0,
		},
		// The zero-items guard: an empty document is a quiet feed, not a
		// badly dated one, so coverage is 1.0 rather than a division by
		// zero. rates {0, 1.0} -> median 0.5; probe 5.0 * 1.0 = 5.0;
		// anchor 0.5/1.0 = 0.5.
		{
			name:       "an empty document keeps the full boost",
			dates:      nil,
			items:      0,
			dated:      0,
			wantProbe:  5.0,
			wantAnchor: 0.5,
		},
		// Coverage scales a non-dormant weight too, not just the cap.
		// probe rate = 1 in-window date / 4 = 0.25/wk; rates {0.25, 1.0} ->
		// median (0.25+1.0)/2 = 0.625. probe computed = 0.625/0.25 = 2.5;
		// coverage 2/4 = 0.5 -> 1.25. anchor = 0.625/1.0 = 0.625 * 1.0.
		{
			name:       "coverage scales a computed weight, not only the cap",
			dates:      datesWithin(1),
			items:      4,
			dated:      2,
			wantProbe:  1.25,
			wantAnchor: 0.625,
		},
		// Defensive clamp: a stored pair that disagrees with itself (more
		// dated than items — unreachable from the fetcher, which counts
		// both over the same slice) must not multiply the weight ABOVE the
		// cap. 5/2 = 2.5 clamps to 1.0, so probe 5.0 * 1.0 = 5.0;
		// rates {0, 1.0} -> median 0.5 -> anchor 0.5.
		{
			name:       "a dated count above the item count clamps to full coverage",
			dates:      datesExpired(2),
			items:      2,
			dated:      5,
			wantProbe:  5.0,
			wantAnchor: 0.5,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &feedstate.File{Version: feedstate.SchemaVersion, Feeds: []feedstate.Feed{
				{
					URL:          "probe",
					FirstSeen:    old,
					ObservedAt:   now,
					PubDates:     tc.dates,
					EverDated:    true,
					LastDocItems: tc.items,
					LastDocDated: tc.dated,
				},
				datedFeed("anchor", old, datesWithin(4)), // 4 in-window dates -> 1.0/wk
			}}
			w := f.Weights([]string{"probe", "anchor"}, now)
			if got := w["probe"]; math.Abs(got-tc.wantProbe) > 1e-9 {
				t.Errorf("probe = %v, want %v", got, tc.wantProbe)
			}
			if got := w["anchor"]; math.Abs(got-tc.wantAnchor) > 1e-9 {
				t.Errorf("anchor = %v, want %v", got, tc.wantAnchor)
			}
		})
	}
}

// TestWeights_CoverageAppliesBeforeTheColdStartBlend pins the ORDER of the
// two adjustments, which the table above cannot see (it runs every case at
// full confidence, where the blend is the identity). Coverage discounts
// the evidence the computed weight is built from, so it belongs to the
// computed weight; the blend then walks that number toward neutral as a
// young feed earns trust. Scaling after the blend would instead discount
// the neutral 1.0 a brand-new feed is entitled to, pushing an unproven
// feed BELOW neutral on nothing but its document's shape.
func TestWeights_CoverageAppliesBeforeTheColdStartBlend(t *testing.T) {
	now := refNow()
	f := &feedstate.File{Version: feedstate.SchemaVersion, Feeds: []feedstate.Feed{
		{
			URL:          "half-covered",
			FirstSeen:    now.Add(-2 * week), // confidence 2wk/4wk = 0.5
			ObservedAt:   now,
			PubDates:     datesExpired(5),
			EverDated:    true,
			LastDocItems: 10,
			LastDocDated: 5,
		},
		datedFeed("anchor", now.Add(-8*week), datesWithin(4)), // 1.0/wk
	}}
	w := f.Weights([]string{"half-covered", "anchor"}, now)
	// rates {0, 1.0} -> median 0.5. Rate 0 -> computed 5.0; coverage
	// 5/10 = 0.5 scales the COMPUTED weight to 2.5; then the blend:
	// 0.5*2.5 + 0.5*1.0 = 1.75. Scaling after the blend instead would
	// give (0.5*5.0 + 0.5*1.0) * 0.5 = 1.5.
	if got := w["half-covered"]; math.Abs(got-1.75) > 1e-9 {
		t.Errorf("half-covered = %v, want 1.75 (coverage scales the computed weight, before the cold-start blend)", got)
	}
}

func TestRead_CorruptErrors(t *testing.T) {
	path := tmp(t)
	if err := os.WriteFile(path, []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := feedstate.Read(path); err == nil {
		t.Error("Read(corrupt) = nil error, want parse error")
	}
}
