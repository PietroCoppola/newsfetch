package onboard

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/huh"

	"github.com/PietroCoppola/newsfetch/internal/config"
	"github.com/PietroCoppola/newsfetch/internal/defaults"
)

// TestDefaultInitAnswers_ValidatesCleanly pins the regression where the
// interactive --init wizard persisted zero-valued count/ticker fields.
// The wizard's form binds only Topics and Style, and renderConfigTOML
// persists count/ticker_marker/ticker_boxed unconditionally, so the
// wizard's starting Answers — written as-is, the "accept everything"
// path — must round-trip through config.Load + config.Validate with no
// warnings and no clamping.
func TestDefaultInitAnswers_ValidatesCleanly(t *testing.T) {
	a := defaultInitAnswers()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteConfig(path, a); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	var buf bytes.Buffer
	got := config.Validate(cfg, config.FieldSources{Style: "config", Count: "config"}, &buf)
	if buf.Len() != 0 {
		t.Errorf("wizard-default config produced validation warnings: %s", buf.String())
	}
	if got.Count != defaults.Count {
		t.Errorf("Count = %d, want default %d", got.Count, defaults.Count)
	}
	if got.TickerMarker != defaults.TickerMarker {
		t.Errorf("TickerMarker = %q, want default %q", got.TickerMarker, defaults.TickerMarker)
	}
	if got.Style != defaults.Style {
		t.Errorf("Style = %q, want default %q", got.Style, defaults.Style)
	}
}

func TestDefaultInitAnswers_SeedsFollowingDisabled(t *testing.T) {
	// --init never asks about pools or feeds, so its seeded answers are what
	// the very first config.toml says about them. Following starts DISABLED
	// with no feeds: a first-run user gets the working news render they came
	// for, and an empty Following box would just be noise.
	a := defaultInitAnswers()
	if !reflect.DeepEqual(a.Pools, defaults.Pools()) {
		t.Errorf("Pools = %v, want %v", a.Pools, defaults.Pools())
	}
	if !reflect.DeepEqual(a.Pools, []string{"news"}) {
		t.Errorf("Pools = %v, want [news] (following must not be enabled by default)", a.Pools)
	}
	if a.PoolOrder != nil {
		t.Errorf("PoolOrder = %v, want nil (one pool has nothing to order)", a.PoolOrder)
	}
	if a.Feeds != nil {
		t.Errorf("Feeds = %v, want nil", a.Feeds)
	}
	if a.NewsAggregators != nil {
		t.Errorf("NewsAggregators = %v, want nil so the writer omits [news]", a.NewsAggregators)
	}
	if a.FollowingCount != defaults.FollowingCount {
		t.Errorf("FollowingCount = %d, want %d", a.FollowingCount, defaults.FollowingCount)
	}
}

// TestInitFields_UnchangedTwoQuestionContract pins the --init form's field
// set. --init asks about topics and style and nothing else; every pool,
// feed and count knob is reachable only through --settings or JSON. A
// wizard that grows a field at a time is how a 15-second onboarding becomes
// a 90-second one, so adding a field here should have to delete a test that
// says why it exists.
func TestInitFields_UnchangedTwoQuestionContract(t *testing.T) {
	a := defaultInitAnswers()
	fields := initFields(&a)
	if len(fields) != 2 {
		t.Fatalf("initFields returned %d fields, want exactly 2 (topics, style)", len(fields))
	}
	if _, ok := fields[0].(*huh.MultiSelect[string]); !ok {
		t.Errorf("field 0 = %T, want *huh.MultiSelect[string] (topics)", fields[0])
	}
	if _, ok := fields[1].(*huh.Select[string]); !ok {
		t.Errorf("field 1 = %T, want *huh.Select[string] (style)", fields[1])
	}
}

// TestStyleOptions_NeverOffersStatusline pins one of the three guards that
// keep statusline flag-only. The other two live in config.Validate and
// onboard.validateStyle; all three must hold, because a persisted
// statusline style makes every terminal open render a bare linked line, or
// nothing at all on a cold cache.
func TestStyleOptions_NeverOffersStatusline(t *testing.T) {
	for _, opt := range styleOptions() {
		if opt.Value == "statusline" {
			t.Fatalf("styleOptions offers statusline: %+v", opt)
		}
	}
}

func TestPoolOptions_OffersNewsAndFollowingOnly(t *testing.T) {
	opts := poolOptions()
	var values []string
	for _, o := range opts {
		values = append(values, o.Value)
		if o.Key == "" {
			t.Errorf("pool option %q has no label", o.Value)
		}
	}
	if !reflect.DeepEqual(values, []string{"news", "following"}) {
		t.Errorf("pool options = %v, want [news following]", values)
	}
	for _, v := range values {
		if v == "repos" {
			t.Error("repos must not be offered: the pool is designed but not implemented")
		}
	}
	if opts[0].Key != defaults.PoolLabel("news") || opts[1].Key != defaults.PoolLabel("following") {
		t.Errorf("labels must come from defaults.PoolLabel; got %q and %q", opts[0].Key, opts[1].Key)
	}
}

func TestPoolFirstOptions(t *testing.T) {
	cases := []struct {
		name  string
		pools []string
		want  []string
	}{
		{"both", []string{"news", "following"}, []string{"news", "following"}},
		{"reordered input", []string{"following", "news"}, []string{"following", "news"}},
		{"single", []string{"news"}, []string{"news"}},
		{"none", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, o := range poolFirstOptions(tc.pools) {
				got = append(got, o.Value)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("poolFirstOptions(%v) values = %v, want %v", tc.pools, got, tc.want)
			}
		})
	}
}

func TestTruncateURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"short", "https://a.example/f", 20, "https://a.example/f"},
		{"exactly at max", "https://a.example/f", 19, "https://a.example/f"},
		{"one over", "https://a.example/fx", 19, "https://a.example/…"},
		{"long", "https://blog.cloudflare.com/rss/very/deep/path/feed.xml", 20, "https://blog.cloudfl…"[:19] + "…"},
		{"empty", "", 10, ""},
		{"max one", "https://a.example/f", 1, "…"},
		{"max zero", "https://a.example/f", 0, "…"},
		{"multibyte not split", "https://exämple.test/ünicode/feed", 12, "https://exä…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateURL(tc.in, tc.max)
			if got != tc.want {
				t.Errorf("truncateURL(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
			if r := []rune(got); len(r) > tc.max && tc.max > 0 {
				t.Errorf("truncateURL(%q, %d) = %q is %d runes, over the cap", tc.in, tc.max, got, len(r))
			}
		})
	}
}

func TestFeedRemoveOptions_LabelTruncatedValueFull(t *testing.T) {
	long := "https://blog.cloudflare.com/rss/a/very/long/path/that/will/not/fit/in/eighty/columns/feed.xml"
	opts := feedRemoveOptions([]Feed{
		{URL: "https://a.example/f"},
		{URL: long},
	})
	if len(opts) != 2 {
		t.Fatalf("got %d options, want 2", len(opts))
	}
	// The VALUE must stay the full URL: removal matches on it exactly, and a
	// truncated value would silently fail to remove the feed the user picked.
	if opts[1].Value != long {
		t.Errorf("option value = %q, want the untruncated URL", opts[1].Value)
	}
	if opts[1].Key == long {
		t.Error("long label was not truncated for display")
	}
	if len([]rune(opts[1].Key)) > feedLabelWidth {
		t.Errorf("label is %d runes, over the %d cap", len([]rune(opts[1].Key)), feedLabelWidth)
	}
	if opts[0].Key != "https://a.example/f" {
		t.Errorf("short label = %q, want it untouched", opts[0].Key)
	}
}

func TestRemoveFeeds(t *testing.T) {
	two := 2
	weight := 0.3
	base := []Feed{
		{URL: "https://a.example/f"},
		{URL: "https://b.example/f", MaxItems: &two, Weight: &weight},
		{URL: "https://c.example/f"},
	}
	cases := []struct {
		name string
		urls []string
		want []Feed
	}{
		{"none", nil, base},
		{"empty slice", []string{}, base},
		{"first", []string{"https://a.example/f"}, []Feed{base[1], base[2]}},
		{"middle", []string{"https://b.example/f"}, []Feed{base[0], base[2]}},
		{"two at once", []string{"https://a.example/f", "https://c.example/f"}, []Feed{base[1]}},
		{"all", []string{"https://a.example/f", "https://b.example/f", "https://c.example/f"}, nil},
		{"unknown url is a no-op", []string{"https://z.example/f"}, base},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := removeFeeds(base, tc.urls)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("removeFeeds(%v) = %+v, want %+v", tc.urls, got, tc.want)
			}
		})
	}
	// The advanced knobs the wizard never surfaces must survive untouched:
	// removing feed A cannot be allowed to rewrite feed B.
	got := removeFeeds(base, []string{"https://a.example/f"})
	if got[0].MaxItems == nil || *got[0].MaxItems != 2 {
		t.Errorf("survivor lost max_items: %+v", got[0])
	}
	if got[0].Weight == nil || *got[0].Weight != 0.3 {
		t.Errorf("survivor lost weight: %+v", got[0])
	}
}

func TestAppendFeed(t *testing.T) {
	base := []Feed{{URL: "https://a.example/f"}}
	cases := []struct {
		name string
		in   []Feed
		url  string
		want []Feed
	}{
		{"appends to empty", nil, "https://a.example/f", []Feed{{URL: "https://a.example/f"}}},
		{"appends to existing", base, "https://b.example/f", []Feed{{URL: "https://a.example/f"}, {URL: "https://b.example/f"}}},
		{"trims surrounding space", base, "  https://b.example/f\t", []Feed{{URL: "https://a.example/f"}, {URL: "https://b.example/f"}}},
		{"duplicate is a no-op", base, "https://a.example/f", base},
		{"duplicate after trim is a no-op", base, " https://a.example/f ", base},
		{"empty is a no-op", base, "", base},
		{"whitespace is a no-op", base, "   ", base},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := appendFeed(tc.in, tc.url)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("appendFeed(%+v, %q) = %+v, want %+v", tc.in, tc.url, got, tc.want)
			}
		})
	}
	// A new feed carries no advanced knobs: the wizard cannot invent a
	// max_items or weight the user was never asked about.
	got := appendFeed(nil, "https://a.example/f")
	if got[0].MaxItems != nil || got[0].Weight != nil {
		t.Errorf("new feed = %+v, want nil knobs", got[0])
	}
}

func TestOrderWithFirst(t *testing.T) {
	cases := []struct {
		name  string
		first string
		pools []string
		want  []string
	}{
		{"following first", "following", []string{"news", "following"}, []string{"following", "news"}},
		{"news first", "news", []string{"news", "following"}, []string{"news", "following"}},
		{"input order ignored", "news", []string{"following", "news"}, []string{"news", "following"}},
		{"single pool", "news", []string{"news"}, []string{"news"}},
		{"first not enabled falls back to compile-time order", "following", []string{"news"}, []string{"news"}},
		{"first empty falls back to compile-time order", "", []string{"news", "following"}, []string{"following", "news"}},
		{"no pools", "news", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := orderWithFirst(tc.first, tc.pools)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("orderWithFirst(%q, %v) = %v, want %v", tc.first, tc.pools, got, tc.want)
			}
		})
	}
}

func TestTickerGroupHidden(t *testing.T) {
	// The ticker knobs are box chrome for stacked stories. With two pools,
	// EITHER count crossing 1 makes them relevant: checking only Count would
	// hide the marker picker from a user whose single news story sits above
	// three following stories.
	cases := []struct {
		name string
		a    Answers
		want bool
	}{
		{"boxed, both counts 1", Answers{Style: "boxed", Count: 1, FollowingCount: 1}, true},
		{"boxed, news count 2", Answers{Style: "boxed", Count: 2, FollowingCount: 1}, false},
		{"boxed, following count 3", Answers{Style: "boxed", Count: 1, FollowingCount: 3}, false},
		{"boxed, both above 1", Answers{Style: "boxed", Count: 2, FollowingCount: 2}, false},
		{"minimal, counts high", Answers{Style: "minimal", Count: 4, FollowingCount: 4}, true},
		{"json, counts high", Answers{Style: "json", Count: 4, FollowingCount: 4}, true},
		{"boxed, zero counts", Answers{Style: "boxed"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tickerGroupHidden(tc.a); got != tc.want {
				t.Errorf("tickerGroupHidden(%+v) = %v, want %v", tc.a, got, tc.want)
			}
		})
	}
}

// TestRequirePoolContent pins ruling R-39's wizard half. The rule is
// symmetric on purpose: the previous shape required a News aggregator
// unconditionally — so a user who unchecked the News pool could not finish
// the wizard at all — while letting an enabled Following pool be saved with
// zero feeds, which is the enabled-but-empty state config.Validate clamps
// and the JSON readers reject. Both directions are asserted here because
// each one was wrong in a different way.
func TestRequirePoolContent(t *testing.T) {
	cases := []struct {
		name       string
		pools      []string
		pool       string
		newsN      int
		followingN int
		wantErr    bool
		wantSub    string
	}{
		{"news enabled and empty", []string{"news"}, "news", 0, 0, true, "aggregator"},
		{"news enabled with one", []string{"news"}, "news", 1, 0, false, ""},
		{"news disabled and empty", []string{"following"}, "news", 0, 0, false, ""},
		{"news disabled, no pools at all", nil, "news", 0, 0, false, ""},
		{"following enabled and empty", []string{"news", "following"}, "following", 0, 0, true, "feed"},
		{"following enabled with one", []string{"following"}, "following", 0, 1, false, ""},
		{"following disabled and empty", []string{"news"}, "following", 0, 0, false, ""},
		// The relaxed half of R-39 (this fix): the rule is "at least one
		// enabled pool has content", not "every enabled pool has content".
		// A user running both pools with one empty and the other full must
		// be able to save — validating the news field must not block on
		// news being empty when following already has feeds, and vice
		// versa.
		{"news empty but following has feeds", []string{"news", "following"}, "news", 0, 3, false, ""},
		{"following empty but news has aggregators", []string{"news", "following"}, "following", 2, 0, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requirePoolContent(tc.pools, tc.pool, tc.newsN, tc.followingN)
			if tc.wantErr && err == nil {
				t.Fatalf("requirePoolContent(%v, %q, %d, %d) = nil, want an error", tc.pools, tc.pool, tc.newsN, tc.followingN)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("requirePoolContent(%v, %q, %d, %d) = %v, want nil", tc.pools, tc.pool, tc.newsN, tc.followingN, err)
			}
			if tc.wantErr && !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q should name what is missing (%q)", err, tc.wantSub)
			}
			if tc.wantErr && !strings.Contains(err.Error(), "off") {
				t.Errorf("error %q should offer the escape: turn the pool off", err)
			}
		})
	}
}
