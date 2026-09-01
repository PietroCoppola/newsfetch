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

// Every Feed literal in the weighting tests below sets SeenDated: true —
// these feeds have all demonstrated a cadence at some point, which is what
// makes a current rate of 0 mean "went quiet" (dormant, boosted) rather
// than "never carried a date" (no signal, neutral). The two cases that
// turn on the flag itself are TestWeights_NeverDatedFeedIsNeutralNotDormant
// and TestWeights_OnceDatedFeedStillEarnsDormantBoost.
func TestWeights(t *testing.T) {
	now := refNow()
	old := now.Add(-8 * week) // 8 weeks: full confidence
	expired := []time.Time{now.Add(-5 * week), now.Add(-6 * week), now.Add(-7 * week)}
	f := &feedstate.File{Version: feedstate.SchemaVersion, Feeds: []feedstate.Feed{
		{URL: "busy", FirstSeen: old, PubDates: datesWithin(16), ObservedAt: now, SeenDated: true},  // 4.0/wk
		{URL: "median", FirstSeen: old, PubDates: datesWithin(4), ObservedAt: now, SeenDated: true}, // 1.0/wk
		{URL: "rare", FirstSeen: old, PubDates: datesWithin(1), ObservedAt: now, SeenDated: true},   // 0.25/wk
		{URL: "dormant", FirstSeen: old, PubDates: expired, ObservedAt: now, SeenDated: true},       // 0/wk: rolled out
		{URL: "unconfigured", FirstSeen: old, PubDates: datesWithin(9), ObservedAt: now, SeenDated: true},
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
				{URL: "anchor", FirstSeen: old, PubDates: datesWithin(4), ObservedAt: now, SeenDated: true}, // 1.0/wk
				{URL: "probe", FirstSeen: old, PubDates: tc.dates, ObservedAt: now, SeenDated: true},
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
		{URL: "d1", FirstSeen: old, ObservedAt: now, SeenDated: true},                                            // 0/wk
		{URL: "d2", FirstSeen: old, PubDates: []time.Time{now.Add(-5 * week)}, ObservedAt: now, SeenDated: true}, // 0/wk
		{URL: "busy", FirstSeen: old, PubDates: datesWithin(16), ObservedAt: now, SeenDated: true},               // 4.0/wk
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
		{URL: "d1", FirstSeen: old, ObservedAt: now, SeenDated: true},
		{URL: "d2", FirstSeen: old, ObservedAt: now, SeenDated: true},
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
		{URL: "new-rare", FirstSeen: twoWeeks, PubDates: datesWithin(1), ObservedAt: now, SeenDated: true}, // 0.25/wk
		{URL: "anchor", FirstSeen: old, PubDates: datesWithin(4), ObservedAt: now, SeenDated: true},        // 1.0/wk
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
		{URL: "skewed", FirstSeen: now.Add(week), PubDates: datesWithin(16), ObservedAt: now, SeenDated: true},
		{URL: "anchor", FirstSeen: old, PubDates: datesWithin(4), ObservedAt: now, SeenDated: true},
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
		{URL: "https://a/feed", PubDates: []time.Time{now.Add(-time.Hour)}, DatesKnown: true},
		{URL: "https://b/feed", PubDates: []time.Time{
			later.Add(-1 * time.Hour), later.Add(-2 * time.Hour),
			later.Add(-3 * time.Hour), later.Add(-4 * time.Hour),
		}, DatesKnown: true},
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
		{URL: "https://undated/feed", DatesKnown: true}, // 200, not one parseable date
		{URL: "https://dated/feed", PubDates: datesWithin(4), DatesKnown: true},
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

func TestWeights_OnceDatedFeedStillEarnsDormantBoost(t *testing.T) {
	now := refNow()
	// The other half of the rule: the 5.0 exists for a feed that showed a
	// cadence and went quiet. The flag is recorded on the first pass and
	// must survive the round-trip through feeds.json — and must not be
	// cleared by a later fetch that carries no dates.
	path := tmp(t)
	later := now.Add(6 * week)
	cfg := []string{"https://a/feed", "https://b/feed"}
	bDates := []time.Time{
		later.Add(-1 * time.Hour), later.Add(-2 * time.Hour),
		later.Add(-3 * time.Hour), later.Add(-4 * time.Hour),
	}
	if err := feedstate.Update(path, cfg, []feedstate.Observation{
		{URL: "https://a/feed", PubDates: []time.Time{now.Add(-time.Hour)}, DatesKnown: true},
		{URL: "https://b/feed", PubDates: bDates, DatesKnown: true},
	}, now); err != nil {
		t.Fatal(err)
	}
	// Six weeks on, a's document still parses but carries nothing dated.
	if err := feedstate.Update(path, cfg, []feedstate.Observation{
		{URL: "https://a/feed", DatesKnown: true},
		{URL: "https://b/feed", PubDates: bDates, DatesKnown: true},
	}, later); err != nil {
		t.Fatal(err)
	}
	f, err := feedstate.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	w := f.Weights(cfg, later)
	// rates {0 (a), 1.0 (b)} → median 0.5; a is dormant, b is 0.5/1.0.
	if got := w["https://a/feed"]; got != 5.0 {
		t.Errorf("a = %v, want 5.0 (a feed that once reported dates stays dormant-eligible across a round-trip)", got)
	}
	if got := w["https://b/feed"]; math.Abs(got-0.5) > 1e-9 {
		t.Errorf("b = %v, want 0.5", got)
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
