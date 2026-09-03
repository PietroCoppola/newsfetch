package onboard

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/defaults"
)

func TestReadInitJSON_Valid(t *testing.T) {
	got, err := ReadInitJSON(strings.NewReader(`{"topics":["rust","ai"],"style":"boxed"}`))
	if err != nil {
		t.Fatalf("ReadInitJSON: %v", err)
	}
	// Omitted optional fields fall back to compile-time defaults;
	// NewsAggregators and Feeds stay nil so the writer omits their tables.
	want := Answers{
		Topics:          []string{"rust", "ai"},
		Style:           "boxed",
		Pools:           defaults.Pools(),
		Count:           defaults.Count,
		FollowingCount:  defaults.FollowingCount,
		TickerMarker:    defaults.TickerMarker,
		TickerBoxed:     defaults.TickerBoxed,
		CacheTTLMinutes: int(defaults.CacheTTL / time.Minute),
		MinPoints:       defaults.MinPoints,
		DedupTTLHours:   int(defaults.DedupWindow / time.Hour),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got.NewsAggregators != nil {
		t.Errorf("NewsAggregators should be nil when omitted; got %v", got.NewsAggregators)
	}
	if got.Feeds != nil {
		t.Errorf("Feeds should be nil when omitted; got %v", got.Feeds)
	}
	if got.PoolOrder != nil {
		t.Errorf("PoolOrder should be nil when omitted; got %v", got.PoolOrder)
	}
}

func TestReadInitJSON_FullSchema(t *testing.T) {
	body := `{
		"topics": ["rust", "ai"],
		"style": "boxed",
		"pools": ["news", "following"],
		"pool_order": ["following", "news"],
		"count": 2,
		"following_count": 3,
		"ticker_marker": "branch",
		"ticker_boxed": true,
		"cache_ttl_minutes": 45,
		"min_points": 10,
		"dedup_ttl_hours": 3,
		"news": {"aggregators": ["hackernews", "lobsters"]},
		"following": {"feeds": [
			{"url": "https://drewdevault.com/blog/index.xml"},
			{"url": "https://blog.cloudflare.com/rss/", "max_items": 2, "weight": 0.3}
		]}
	}`
	got, err := ReadInitJSON(strings.NewReader(body))
	if err != nil {
		t.Fatalf("ReadInitJSON: %v", err)
	}
	two := 2
	tenth := 0.3
	want := Answers{
		Topics:          []string{"rust", "ai"},
		Style:           "boxed",
		Pools:           []string{"news", "following"},
		PoolOrder:       []string{"following", "news"},
		NewsAggregators: []string{"hackernews", "lobsters"},
		Count:           2,
		FollowingCount:  3,
		Feeds: []Feed{
			{URL: "https://drewdevault.com/blog/index.xml"},
			{URL: "https://blog.cloudflare.com/rss/", MaxItems: &two, Weight: &tenth},
		},
		TickerMarker:    "branch",
		TickerBoxed:     true,
		CacheTTLMinutes: 45,
		MinPoints:       10,
		DedupTTLHours:   3,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestReadInitJSON_PoolsValidated(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantSub string
	}{
		{"empty", `{"topics":[],"style":"boxed","pools":[]}`, "non-empty"},
		{"unknown name", `{"topics":[],"style":"boxed","pools":["repos"]}`, `"repos"`},
		{"duplicate", `{"topics":[],"style":"boxed","pools":["news","news"]}`, "duplicate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadInitJSON(strings.NewReader(tc.body))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q should contain %q", err, tc.wantSub)
			}
		})
	}
}

func TestReadInitJSON_PoolOrderValidated(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantSub string
	}{
		{
			"entry not enabled",
			`{"topics":[],"style":"boxed","pools":["news"],"pool_order":["following"]}`,
			"not an enabled pool",
		},
		{
			"duplicate entry",
			`{"topics":[],"style":"boxed","pools":["news","following"],"pool_order":["news","news"]}`,
			"duplicate",
		},
		{
			"unknown entry",
			`{"topics":[],"style":"boxed","pools":["news"],"pool_order":["repos"]}`,
			`"repos"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadInitJSON(strings.NewReader(tc.body))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q should contain %q", err, tc.wantSub)
			}
		})
	}
}

func TestReadInitJSON_PoolOrderPartialAccepted(t *testing.T) {
	// A partial order is fine: config.Validate appends the missing pools in
	// compile-time order. Rejecting it would force scripts to restate an
	// ordering the loader already knows how to complete.
	got, err := ReadInitJSON(strings.NewReader(
		`{"topics":[],"style":"boxed","pools":["news","following"],"pool_order":["following"]}`,
	))
	if err != nil {
		t.Fatalf("ReadInitJSON: %v", err)
	}
	if !reflect.DeepEqual(got.PoolOrder, []string{"following"}) {
		t.Errorf("PoolOrder = %v, want [following]", got.PoolOrder)
	}
}

func TestReadInitJSON_FollowingCountOutOfRange(t *testing.T) {
	cases := []string{
		`{"topics":[],"style":"boxed","following_count":0}`,
		`{"topics":[],"style":"boxed","following_count":99}`,
	}
	for _, body := range cases {
		_, err := ReadInitJSON(strings.NewReader(body))
		if err == nil {
			t.Errorf("expected error for body %q", body)
			continue
		}
		if !strings.Contains(err.Error(), "following_count") {
			t.Errorf("error should name the field; got %v", err)
		}
	}
}

func TestReadInitJSON_FeedsValidated(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantSub string
	}{
		{
			"missing url",
			`{"topics":[],"style":"boxed","following":{"feeds":[{"max_items":2}]}}`,
			`"url"`,
		},
		{
			"empty url",
			`{"topics":[],"style":"boxed","following":{"feeds":[{"url":""}]}}`,
			"empty",
		},
		{
			"non-http scheme",
			`{"topics":[],"style":"boxed","following":{"feeds":[{"url":"ftp://x.example/f.xml"}]}}`,
			"http",
		},
		{
			"max_items too low",
			`{"topics":[],"style":"boxed","following":{"feeds":[{"url":"https://a.example/f","max_items":0}]}}`,
			"max_items",
		},
		{
			"max_items too high",
			`{"topics":[],"style":"boxed","following":{"feeds":[{"url":"https://a.example/f","max_items":11}]}}`,
			"max_items",
		},
		{
			"weight zero",
			`{"topics":[],"style":"boxed","following":{"feeds":[{"url":"https://a.example/f","weight":0}]}}`,
			"weight",
		},
		{
			"weight above cap",
			`{"topics":[],"style":"boxed","following":{"feeds":[{"url":"https://a.example/f","weight":5.1}]}}`,
			"weight",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadInitJSON(strings.NewReader(tc.body))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q should contain %q", err, tc.wantSub)
			}
		})
	}
}

func TestReadInitJSON_FeedBoundsAreInclusive(t *testing.T) {
	// The edges of [1, 10] and (0, 5.0] must be accepted; an off-by-one in
	// the comparison shows up here and nowhere else.
	body := `{"topics":[],"style":"boxed","following":{"feeds":[
		{"url":"https://a.example/f","max_items":1,"weight":0.01},
		{"url":"https://b.example/f","max_items":10,"weight":5.0}
	]}}`
	got, err := ReadInitJSON(strings.NewReader(body))
	if err != nil {
		t.Fatalf("ReadInitJSON: %v", err)
	}
	if len(got.Feeds) != 2 {
		t.Fatalf("Feeds = %+v, want two", got.Feeds)
	}
	if *got.Feeds[0].MaxItems != 1 || *got.Feeds[1].MaxItems != 10 {
		t.Errorf("max_items bounds not carried: %+v", got.Feeds)
	}
	if *got.Feeds[1].Weight != defaults.MaxFeedWeight {
		t.Errorf("weight = %v, want the cap %v", *got.Feeds[1].Weight, defaults.MaxFeedWeight)
	}
}

// TestValidateFeeds_NonFiniteWeightRejected goes at validateFeeds directly
// rather than through ReadInitJSON, because encoding/json will not decode a
// bare NaN or Infinity token — the JSON grammar has no literal for either.
// The values still arrive: the same []Feed is built by --settings from a
// TOML config, where `weight = nan` is a perfectly ordinary float literal.
// The check matters because NaN compares false against EVERYTHING, so the
// obvious `*f.Weight <= 0 || *f.Weight > MaxFeedWeight` range test passes it
// straight through.
func TestValidateFeeds_NonFiniteWeightRejected(t *testing.T) {
	for _, w := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		weight := w
		err := validateFeeds("--init", []Feed{{URL: "https://a.example/f", Weight: &weight}})
		if err == nil {
			t.Errorf("validateFeeds accepted a non-finite weight %v", w)
			continue
		}
		if !strings.Contains(err.Error(), "finite") {
			t.Errorf("error %q should say the weight must be finite", err)
		}
	}
}

func TestReadInitJSON_UnknownFieldRejectedInNestedObjects(t *testing.T) {
	// encoding/json applies DisallowUnknownFields at every nesting depth, but
	// only while the nested value decodes into a real struct. If someone
	// later loosens news/following/feeds to map[string]any or
	// json.RawMessage, these cases go green-to-silent — which is exactly the
	// regression this test exists to catch.
	cases := []struct {
		name string
		body string
	}{
		{"top level", `{"topics":[],"style":"boxed","refresh_interval":10}`},
		{"inside news", `{"topics":[],"style":"boxed","news":{"aggregators":["hackernews"],"bogus":1}}`},
		{"inside following", `{"topics":[],"style":"boxed","following":{"feeds":[],"bogus":1}}`},
		{"inside a feed", `{"topics":[],"style":"boxed","following":{"feeds":[{"url":"https://a.example/f","bogus":1}]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadInitJSON(strings.NewReader(tc.body))
			if err == nil {
				t.Fatal("expected error for unknown field")
			}
			if !strings.Contains(err.Error(), "unknown field") {
				t.Errorf("error should be an unknown-field rejection; got %v", err)
			}
		})
	}
}

// TestReadInitJSON_SourcesKeyIsADeliberateBreak documents ruling R-4. The
// legacy "sources" key is GONE from the JSON contract with no alias and no
// deprecation path; DisallowUnknownFields rejecting it by name is the whole
// migration story. TOML keeps its read-time alias because it protects
// config files that exist on disk today — see the comment in ReadInitJSON.
// If you are here because this test failed, someone added an alias back.
func TestReadInitJSON_SourcesKeyIsADeliberateBreak(t *testing.T) {
	_, err := ReadInitJSON(strings.NewReader(`{"topics":[],"style":"boxed","sources":["hackernews"]}`))
	if err == nil {
		t.Fatal("expected the removed sources key to be rejected")
	}
	if !strings.Contains(err.Error(), "sources") {
		t.Errorf("error should name the offending key; got %v", err)
	}
}

func TestReadInitJSON_CountOutOfRange(t *testing.T) {
	cases := []string{
		`{"topics":[],"style":"boxed","count":0}`,
		`{"topics":[],"style":"boxed","count":99}`,
	}
	for _, body := range cases {
		_, err := ReadInitJSON(strings.NewReader(body))
		if err == nil {
			t.Errorf("expected error for body %q", body)
		}
	}
}

func TestReadInitJSON_UnknownTickerMarker(t *testing.T) {
	_, err := ReadInitJSON(strings.NewReader(`{"topics":[],"style":"boxed","ticker_marker":"spiral"}`))
	if err == nil {
		t.Fatal("expected error for unknown ticker_marker")
	}
}

func TestReadInitJSON_EmptyTopicsAllowed(t *testing.T) {
	got, err := ReadInitJSON(strings.NewReader(`{"topics":[],"style":"minimal"}`))
	if err != nil {
		t.Fatalf("ReadInitJSON: %v", err)
	}
	if len(got.Topics) != 0 {
		t.Errorf("Topics = %v, want empty", got.Topics)
	}
	if got.Style != "minimal" {
		t.Errorf("Style = %q, want minimal", got.Style)
	}
}

func TestReadInitJSON_AggregatorsOptional_PowerUser(t *testing.T) {
	got, err := ReadInitJSON(strings.NewReader(
		`{"topics":[],"style":"boxed","news":{"aggregators":["hackernews","lobsters"]}}`,
	))
	if err != nil {
		t.Fatalf("ReadInitJSON: %v", err)
	}
	if !reflect.DeepEqual(got.NewsAggregators, []string{"hackernews", "lobsters"}) {
		t.Errorf("NewsAggregators = %v, want [hackernews lobsters]", got.NewsAggregators)
	}
}

// TestReadInitJSON_AggregatorsEmptyAccepted pins rulings R-8 and R-39
// together: an empty aggregator list is legal input at the JSON boundary
// because it is legal in TOML, and the two surfaces must not disagree about
// what the same list means. What is illegal is the COMBINATION that leaves
// nothing to render, and the test below this one owns that rule — which is
// why this fixture keeps a second pool with content in it.
func TestReadInitJSON_AggregatorsEmptyAccepted(t *testing.T) {
	got, err := ReadInitJSON(strings.NewReader(
		`{"topics":[],"style":"boxed","pools":["news","following"],` +
			`"news":{"aggregators":[]},` +
			`"following":{"feeds":[{"url":"https://a.example/f"}]}}`,
	))
	if err != nil {
		t.Fatalf("empty aggregators must be accepted (R-8); got %v", err)
	}
	if got.NewsAggregators == nil {
		t.Fatal("an explicit [] must stay non-nil: nil means omitted, which the writer turns back into the default list")
	}
	if len(got.NewsAggregators) != 0 {
		t.Errorf("NewsAggregators = %v, want an empty list", got.NewsAggregators)
	}
}

// TestReadInitJSON_AllEnabledPoolsEmptyRejected owns the cross-field rule
// (R-39). TOML clamps this state back to the defaults with a warning (R-9);
// JSON fails loud, so a script is told rather than quietly handed a config
// it did not ask for.
func TestReadInitJSON_AllEnabledPoolsEmptyRejected(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantSub string
	}{
		{
			"news enabled with an explicit empty list",
			`{"topics":[],"style":"boxed","pools":["news"],"news":{"aggregators":[]}}`,
			"news is enabled with no aggregators",
		},
		{
			"following enabled with no feeds",
			`{"topics":[],"style":"boxed","pools":["following"]}`,
			"following is enabled with no feeds",
		},
		{
			"both enabled, both empty",
			`{"topics":[],"style":"boxed","pools":["news","following"],"news":{"aggregators":[]},"following":{"feeds":[]}}`,
			"following is enabled with no feeds",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadInitJSON(strings.NewReader(tc.body))
			if err == nil {
				t.Fatal("expected the all-empty-pools combination to be rejected")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q should name the empty half: %q", err, tc.wantSub)
			}
			if !strings.Contains(err.Error(), "news.aggregators") || !strings.Contains(err.Error(), "following.feeds") {
				t.Errorf("error should name both remedies; got %v", err)
			}
		})
	}
}

// TestReadInitJSON_OmittedAggregatorsAreNotEmpty guards the nil-vs-empty
// distinction the cross-field rule rests on. An OMITTED news.aggregators is
// not an empty one: the writer leaves the [news] table out and config.Load
// supplies defaults.Sources(), so the pool is populated. Conflating the two
// would make the plain two-field --init payload — by far the most common
// one — fail outright.
func TestReadInitJSON_OmittedAggregatorsAreNotEmpty(t *testing.T) {
	got, err := ReadInitJSON(strings.NewReader(`{"topics":[],"style":"boxed","pools":["news"]}`))
	if err != nil {
		t.Fatalf("an omitted aggregator list must inherit the default, not fail: %v", err)
	}
	if got.NewsAggregators != nil {
		t.Errorf("NewsAggregators = %v, want nil so the writer omits the table", got.NewsAggregators)
	}
}

func TestReadInitJSON_AggregatorsUnknownRejected(t *testing.T) {
	_, err := ReadInitJSON(strings.NewReader(`{"topics":[],"style":"boxed","news":{"aggregators":["weirdsrc"]}}`))
	if err == nil {
		t.Fatal("expected error for unknown aggregator name")
	}
	if !strings.Contains(err.Error(), "weirdsrc") {
		t.Errorf("error should name the offending aggregator; got %v", err)
	}
}

func TestReadInitJSON_MissingTopics(t *testing.T) {
	_, err := ReadInitJSON(strings.NewReader(`{"style":"boxed"}`))
	if err == nil {
		t.Fatal("expected error for missing topics")
	}
	if !strings.Contains(err.Error(), "topics") {
		t.Errorf("error should name the missing field; got %v", err)
	}
}

func TestReadInitJSON_MissingStyle(t *testing.T) {
	_, err := ReadInitJSON(strings.NewReader(`{"topics":[]}`))
	if err == nil {
		t.Fatal("expected error for missing style")
	}
	if !strings.Contains(err.Error(), "style") {
		t.Errorf("error should name the missing field; got %v", err)
	}
}

func TestReadInitJSON_InvalidStyle(t *testing.T) {
	_, err := ReadInitJSON(strings.NewReader(`{"topics":[],"style":"fancy"}`))
	if err == nil {
		t.Fatal("expected error for invalid style")
	}
	if !strings.Contains(err.Error(), "fancy") {
		t.Errorf("error should name the offending value; got %v", err)
	}
}

func TestReadInitJSON_StatuslineStyleRejected(t *testing.T) {
	// statusline is a flag-only style. This is one of three guards that keep
	// it out of persisted config; all three must survive.
	_, err := ReadInitJSON(strings.NewReader(`{"topics":[],"style":"statusline"}`))
	if err == nil {
		t.Fatal("expected statusline to be rejected as a config style")
	}
}

func TestReadInitJSON_Malformed(t *testing.T) {
	_, err := ReadInitJSON(strings.NewReader(`{ not valid json`))
	if err == nil {
		t.Fatal("expected decode error")
	}
}

func TestValidateFeedURL(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
		wantSub string
	}{
		{"https", "https://drewdevault.com/blog/index.xml", false, ""},
		{"http", "http://example.com/feed", false, ""},
		{"https with query", "https://example.com/feed?format=xml", false, ""},
		{"empty", "", true, "empty"},
		{"whitespace only", "   ", true, "empty"},
		{"relative", "example.com/feed.xml", true, "http"},
		{"ftp scheme", "ftp://example.com/feed.xml", true, "http"},
		{"missing host", "http://", true, "host"},
		{"empty authority", "https:///feed.xml", true, "host"},
		{"unparseable", "http://[::1", true, "parse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateFeedURL(tc.in)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateFeedURL(%q) = nil, want an error", tc.in)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateFeedURL(%q) = %v, want nil", tc.in, err)
			}
			if tc.wantErr && !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q should contain %q", err, tc.wantSub)
			}
		})
	}
}
