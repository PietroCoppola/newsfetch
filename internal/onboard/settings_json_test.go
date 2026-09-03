package onboard

import (
	"reflect"
	"strings"
	"testing"
)

// currFeedMaxItems and currFeedWeight back the pointers in curr. They are
// package-level so the fixture can take their addresses without a helper,
// and so a test asserting inheritance can compare against the same values.
var (
	currFeedMaxItems = 2
	currFeedWeight   = 0.3
)

// curr is the baseline current Answers used for tests where the caller
// would have loaded the existing config. Holds non-default ticker values
// and a feed carrying both advanced knobs so tests can assert preservation
// through omission.
var curr = Answers{
	Topics:          nil,
	Style:           "boxed",
	Pools:           []string{"news", "following"},
	PoolOrder:       []string{"following", "news"},
	NewsAggregators: []string{"hackernews"},
	Count:           1,
	FollowingCount:  2,
	Feeds: []Feed{
		{URL: "https://drewdevault.com/blog/index.xml"},
		{URL: "https://blog.cloudflare.com/rss/", MaxItems: &currFeedMaxItems, Weight: &currFeedWeight},
	},
	TickerMarker: "branch",
	TickerBoxed:  true,
	// Deliberately distinct from both the compile-time defaults (30/50/6)
	// and from any value a test below sends in a payload, so a test that
	// finds these numbers can only have gotten them from curr.
	CacheTTLMinutes: 45,
	MinPoints:       10,
	DedupTTLHours:   3,
}

func TestReadSettingsJSON_Valid(t *testing.T) {
	got, err := ReadSettingsJSON(strings.NewReader(`{
		"topics": ["rust"],
		"style": "boxed",
		"pools": ["news", "following"],
		"pool_order": ["news", "following"],
		"count": 3,
		"following_count": 1,
		"ticker_marker": "arrow",
		"ticker_boxed": false,
		"cache_ttl_minutes": 20,
		"min_points": 5,
		"dedup_ttl_hours": 12,
		"news": {"aggregators": ["hackernews", "lobsters"]},
		"following": {"feeds": [{"url": "https://example.com/feed.xml", "max_items": 4}]}
	}`), curr)
	if err != nil {
		t.Fatalf("ReadSettingsJSON: %v", err)
	}
	four := 4
	want := Answers{
		Topics:          []string{"rust"},
		Style:           "boxed",
		Pools:           []string{"news", "following"},
		PoolOrder:       []string{"news", "following"},
		NewsAggregators: []string{"hackernews", "lobsters"},
		Count:           3,
		FollowingCount:  1,
		Feeds:           []Feed{{URL: "https://example.com/feed.xml", MaxItems: &four}},
		TickerMarker:    "arrow",
		TickerBoxed:     false,
		// Distinct from both curr's values (45/10/3) and the compile-time
		// defaults (30/50/6): only a payload override could have produced
		// these.
		CacheTTLMinutes: 20,
		MinPoints:       5,
		DedupTTLHours:   12,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestReadSettingsJSON_CacheDedupMinPointsInheritCurrentWhenOmitted pins the
// fix for the review finding that --settings silently reverted
// cache_ttl_minutes, min_points, and dedup_ttl_hours to their compile-time
// defaults on every save. An omitted key must inherit the caller's current
// configuration, exactly like following_count/ticker_marker/ticker_boxed
// already do — never fall back to defaults.CacheTTL/MinPoints/DedupWindow,
// which would silently discard a user's tuning the first time they ran
// --settings for something unrelated.
func TestReadSettingsJSON_CacheDedupMinPointsInheritCurrentWhenOmitted(t *testing.T) {
	got, err := ReadSettingsJSON(strings.NewReader(
		`{"topics":[],"style":"minimal","pools":["news","following"],"count":1}`,
	), curr)
	if err != nil {
		t.Fatalf("ReadSettingsJSON: %v", err)
	}
	if got.CacheTTLMinutes != curr.CacheTTLMinutes {
		t.Errorf("CacheTTLMinutes = %d, want %d (preserved from current)", got.CacheTTLMinutes, curr.CacheTTLMinutes)
	}
	if got.MinPoints != curr.MinPoints {
		t.Errorf("MinPoints = %d, want %d (preserved from current)", got.MinPoints, curr.MinPoints)
	}
	if got.DedupTTLHours != curr.DedupTTLHours {
		t.Errorf("DedupTTLHours = %d, want %d (preserved from current)", got.DedupTTLHours, curr.DedupTTLHours)
	}
}

// TestReadSettingsJSON_OmittedPoolInternalsInheritCurrent is the
// persist-don't-clear guarantee for the JSON path, and the JSON-side twin
// of the --settings erasure risk: ReadSettingsJSON's output is written over
// the user's whole config file, so a pool internal that does not survive an
// omission is a pool internal deleted from disk.
func TestReadSettingsJSON_OmittedPoolInternalsInheritCurrent(t *testing.T) {
	got, err := ReadSettingsJSON(strings.NewReader(
		`{"topics":[],"style":"minimal","pools":["news","following"],"count":1}`,
	), curr)
	if err != nil {
		t.Fatalf("ReadSettingsJSON: %v", err)
	}
	if !reflect.DeepEqual(got.NewsAggregators, curr.NewsAggregators) {
		t.Errorf("NewsAggregators = %v, want %v (preserved from current)", got.NewsAggregators, curr.NewsAggregators)
	}
	if !reflect.DeepEqual(got.Feeds, curr.Feeds) {
		t.Errorf("Feeds = %+v, want %+v (preserved from current)", got.Feeds, curr.Feeds)
	}
	if len(got.Feeds) != 2 {
		t.Fatalf("Feeds = %+v, want two", got.Feeds)
	}
	if got.Feeds[0].MaxItems != nil || got.Feeds[0].Weight != nil {
		t.Errorf("bare feed gained knobs it never had: %+v", got.Feeds[0])
	}
	if got.Feeds[1].MaxItems == nil || *got.Feeds[1].MaxItems != 2 {
		t.Errorf("feed max_items not preserved: %+v", got.Feeds[1])
	}
	if got.Feeds[1].Weight == nil || *got.Feeds[1].Weight != 0.3 {
		t.Errorf("feed weight not preserved: %+v", got.Feeds[1])
	}
	if !reflect.DeepEqual(got.PoolOrder, curr.PoolOrder) {
		t.Errorf("PoolOrder = %v, want %v (preserved from current)", got.PoolOrder, curr.PoolOrder)
	}
	if got.FollowingCount != curr.FollowingCount {
		t.Errorf("FollowingCount = %d, want %d (preserved from current)", got.FollowingCount, curr.FollowingCount)
	}
	if got.TickerMarker != curr.TickerMarker {
		t.Errorf("TickerMarker = %q, want %q (preserved from current)", got.TickerMarker, curr.TickerMarker)
	}
	if got.TickerBoxed != curr.TickerBoxed {
		t.Errorf("TickerBoxed = %v, want %v (preserved from current)", got.TickerBoxed, curr.TickerBoxed)
	}
}

func TestReadSettingsJSON_ExplicitEmptyFeedsClears(t *testing.T) {
	// An OMITTED key inherits; an explicitly empty array is the caller
	// saying "remove them all". Conflating the two would leave a scripted
	// user with no way to unsubscribe from everything.
	got, err := ReadSettingsJSON(strings.NewReader(
		`{"topics":[],"style":"boxed","pools":["news"],"count":1,"following":{"feeds":[]}}`,
	), curr)
	if err != nil {
		t.Fatalf("ReadSettingsJSON: %v", err)
	}
	if got.Feeds != nil {
		t.Errorf("Feeds = %+v, want nil after an explicit empty array", got.Feeds)
	}
}

func TestReadSettingsJSON_RequiredFields(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"missing topics", `{"style":"boxed","pools":["news"],"count":1}`},
		{"missing style", `{"topics":[],"pools":["news"],"count":1}`},
		{"missing pools", `{"topics":[],"style":"boxed","count":1}`},
		{"missing count", `{"topics":[],"style":"boxed","pools":["news"]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadSettingsJSON(strings.NewReader(tc.body), curr)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestReadSettingsJSON_PoolsValidated(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantSub string
	}{
		{"empty", `{"topics":[],"style":"boxed","pools":[],"count":1}`, "non-empty"},
		{"unknown", `{"topics":[],"style":"boxed","pools":["repos"],"count":1}`, `"repos"`},
		{"duplicate", `{"topics":[],"style":"boxed","pools":["news","news"],"count":1}`, "duplicate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadSettingsJSON(strings.NewReader(tc.body), curr)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q should contain %q", err, tc.wantSub)
			}
		})
	}
}

func TestReadSettingsJSON_PoolOrderValidated(t *testing.T) {
	_, err := ReadSettingsJSON(strings.NewReader(
		`{"topics":[],"style":"boxed","pools":["news"],"count":1,"pool_order":["following"]}`,
	), curr)
	if err == nil {
		t.Fatal("expected error for a pool_order naming a disabled pool")
	}
	if !strings.Contains(err.Error(), "not an enabled pool") {
		t.Errorf("error should explain the constraint; got %v", err)
	}
}

func TestReadSettingsJSON_FollowingCountOutOfRange(t *testing.T) {
	cases := []string{
		`{"topics":[],"style":"boxed","pools":["news"],"count":1,"following_count":0}`,
		`{"topics":[],"style":"boxed","pools":["news"],"count":1,"following_count":99}`,
	}
	for _, body := range cases {
		_, err := ReadSettingsJSON(strings.NewReader(body), curr)
		if err == nil {
			t.Errorf("expected error for body %q", body)
		}
	}
}

func TestReadSettingsJSON_FeedsValidated(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantSub string
	}{
		{
			"missing url",
			`{"topics":[],"style":"boxed","pools":["news"],"count":1,"following":{"feeds":[{"weight":1.0}]}}`,
			`"url"`,
		},
		{
			"bad scheme",
			`{"topics":[],"style":"boxed","pools":["news"],"count":1,"following":{"feeds":[{"url":"ftp://x.example/f"}]}}`,
			"http",
		},
		{
			"max_items out of range",
			`{"topics":[],"style":"boxed","pools":["news"],"count":1,"following":{"feeds":[{"url":"https://a.example/f","max_items":99}]}}`,
			"max_items",
		},
		{
			"weight out of range",
			`{"topics":[],"style":"boxed","pools":["news"],"count":1,"following":{"feeds":[{"url":"https://a.example/f","weight":-1}]}}`,
			"weight",
		},
		{
			// Same rule as --init, from the same shared validator: the
			// two flags must not disagree about what a feed list may
			// contain.
			"duplicate url",
			`{"topics":[],"style":"boxed","pools":["news"],"count":1,"following":{"feeds":[{"url":"https://a.example/f"},{"url":"https://a.example/f"}]}}`,
			"duplicate url",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadSettingsJSON(strings.NewReader(tc.body), curr)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q should contain %q", err, tc.wantSub)
			}
		})
	}
}

func TestReadSettingsJSON_AggregatorsValidated(t *testing.T) {
	// Only NAMES are validated here. Emptiness is legal (R-8/R-39) and is
	// the cross-field rule's business, not this validator's.
	_, err := ReadSettingsJSON(strings.NewReader(
		`{"topics":[],"style":"boxed","pools":["news"],"count":1,"news":{"aggregators":["weirdsrc"]}}`,
	), curr)
	if err == nil {
		t.Fatal("expected error for an unknown aggregator name")
	}
	if !strings.Contains(err.Error(), "weirdsrc") {
		t.Errorf("error should name the offending aggregator; got %v", err)
	}
}

// TestReadSettingsJSON_AggregatorsEmptyAccepted is the settings-side twin of
// the init test: an explicit empty list is legal input, and stays non-nil so
// the writer records the user's choice rather than reverting to the default.
func TestReadSettingsJSON_AggregatorsEmptyAccepted(t *testing.T) {
	got, err := ReadSettingsJSON(strings.NewReader(
		`{"topics":[],"style":"boxed","pools":["news","following"],"count":1,"news":{"aggregators":[]}}`,
	), curr)
	if err != nil {
		t.Fatalf("empty aggregators must be accepted (R-8); got %v", err)
	}
	if got.NewsAggregators == nil || len(got.NewsAggregators) != 0 {
		t.Errorf("NewsAggregators = %v, want a non-nil empty list", got.NewsAggregators)
	}
}

// TestReadSettingsJSON_AllEnabledPoolsEmptyRejected owns the cross-field
// rule for --settings. The last case is the one only this reader can hit:
// the payload names no aggregators and no feeds at all, so the emptiness
// does not exist until both keys have fallen back to a `current` that is
// itself empty. A validator running before the merge sees a clean payload.
func TestReadSettingsJSON_AllEnabledPoolsEmptyRejected(t *testing.T) {
	emptyCurrent := Answers{
		Style:           "boxed",
		Pools:           []string{"news"},
		NewsAggregators: []string{}, // explicitly empty on disk, which R-8 allows
		Count:           1,
		FollowingCount:  1,
		TickerMarker:    "dot",
	}
	cases := []struct {
		name    string
		body    string
		current Answers
		wantSub string
	}{
		{
			"explicit in the payload",
			`{"topics":[],"style":"boxed","pools":["news"],"count":1,"news":{"aggregators":[]}}`,
			curr,
			"news is enabled with no aggregators",
		},
		{
			"following enabled with the feeds explicitly cleared",
			`{"topics":[],"style":"boxed","pools":["following"],"count":1,"following":{"feeds":[]}}`,
			curr,
			"following is enabled with no feeds",
		},
		{
			"emptiness inherited from current",
			`{"topics":[],"style":"boxed","pools":["news"],"count":1}`,
			emptyCurrent,
			"news is enabled with no aggregators",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadSettingsJSON(strings.NewReader(tc.body), tc.current)
			if err == nil {
				t.Fatal("expected the all-empty-pools combination to be rejected")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q should name the empty half: %q", err, tc.wantSub)
			}
		})
	}
}

// TestReadSettingsJSON_InheritedAggregatorsSatisfyTheCrossFieldRule is the
// negative control for the case above: the same payload, against a current
// whose aggregator list has content, must pass. Without it a validator that
// simply rejected every omitted key would look correct.
func TestReadSettingsJSON_InheritedAggregatorsSatisfyTheCrossFieldRule(t *testing.T) {
	got, err := ReadSettingsJSON(strings.NewReader(
		`{"topics":[],"style":"boxed","pools":["news"],"count":1}`,
	), curr)
	if err != nil {
		t.Fatalf("inherited aggregators must satisfy the rule: %v", err)
	}
	if !reflect.DeepEqual(got.NewsAggregators, curr.NewsAggregators) {
		t.Errorf("NewsAggregators = %v, want %v", got.NewsAggregators, curr.NewsAggregators)
	}
}

func TestReadSettingsJSON_InvalidStyle(t *testing.T) {
	_, err := ReadSettingsJSON(strings.NewReader(
		`{"topics":[],"style":"fancy","pools":["news"],"count":1}`,
	), curr)
	if err == nil {
		t.Fatal("expected error for invalid style")
	}
}

func TestReadSettingsJSON_CountOutOfRange(t *testing.T) {
	cases := []string{
		`{"topics":[],"style":"boxed","pools":["news"],"count":0}`,
		`{"topics":[],"style":"boxed","pools":["news"],"count":99}`,
	}
	for _, body := range cases {
		_, err := ReadSettingsJSON(strings.NewReader(body), curr)
		if err == nil {
			t.Errorf("expected error for body %q", body)
		}
	}
}

func TestReadSettingsJSON_UnknownTickerMarker(t *testing.T) {
	_, err := ReadSettingsJSON(strings.NewReader(
		`{"topics":[],"style":"boxed","pools":["news"],"count":2,"ticker_marker":"spiral"}`,
	), curr)
	if err == nil {
		t.Fatal("expected error for unknown ticker_marker")
	}
	if !strings.Contains(err.Error(), "spiral") {
		t.Errorf("error should name the offending marker; got %v", err)
	}
}

func TestReadSettingsJSON_UnknownFieldRejectedInNestedObjects(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"top level", `{"topics":[],"style":"boxed","pools":["news"],"count":1,"refresh_interval":10}`},
		{"inside news", `{"topics":[],"style":"boxed","pools":["news"],"count":1,"news":{"aggregators":["hackernews"],"bogus":1}}`},
		{"inside following", `{"topics":[],"style":"boxed","pools":["news"],"count":1,"following":{"feeds":[],"bogus":1}}`},
		{"inside a feed", `{"topics":[],"style":"boxed","pools":["news"],"count":1,"following":{"feeds":[{"url":"https://a.example/f","bogus":1}]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadSettingsJSON(strings.NewReader(tc.body), curr)
			if err == nil {
				t.Fatal("expected error for unknown field")
			}
			if !strings.Contains(err.Error(), "unknown field") {
				t.Errorf("error should be an unknown-field rejection; got %v", err)
			}
		})
	}
}

// TestReadSettingsJSON_SourcesKeyIsADeliberateBreak documents ruling R-4:
// the legacy "sources" key is gone from the JSON contract with no alias.
// See the twin test in init_json_test.go and the comment in ReadInitJSON
// for why TOML keeps its alias and JSON does not.
func TestReadSettingsJSON_SourcesKeyIsADeliberateBreak(t *testing.T) {
	_, err := ReadSettingsJSON(strings.NewReader(
		`{"topics":[],"style":"boxed","pools":["news"],"count":1,"sources":["hackernews"]}`,
	), curr)
	if err == nil {
		t.Fatal("expected the removed sources key to be rejected")
	}
	if !strings.Contains(err.Error(), "sources") {
		t.Errorf("error should name the offending key; got %v", err)
	}
}

// TestReadSettingsJSON_AdvancedKnobsValidated is the --settings twin of
// TestReadInitJSON_AdvancedKnobsValidated. It matters more here: a
// --settings save rewrites the user's whole config file, so an unchecked
// value is not merely written once but written over whatever was valid
// before.
func TestReadSettingsJSON_AdvancedKnobsValidated(t *testing.T) {
	const head = `{"topics":[],"style":"boxed","pools":["news"],"count":1,`
	cases := []struct {
		name string
		body string
		// wantErr is the substring the message must name; "" means the
		// value is valid and must be carried through.
		wantErr string
	}{
		{"cache ttl negative", head + `"cache_ttl_minutes":-1}`, "cache_ttl_minutes"},
		{"cache ttl zero", head + `"cache_ttl_minutes":0}`, "cache_ttl_minutes"},
		{"cache ttl one below floor", head + `"cache_ttl_minutes":4}`, "cache_ttl_minutes"},
		{"cache ttl at floor", head + `"cache_ttl_minutes":5}`, ""},
		{"cache ttl above floor", head + `"cache_ttl_minutes":6}`, ""},
		// 153722867 minutes is the largest value that survives Load's
		// cache_ttl_minutes*time.Minute multiplication inside a signed
		// 64-bit nanosecond Duration; one more overflows negative and would
		// be silently clamped to the floor on the next load.
		{"cache ttl at max", head + `"cache_ttl_minutes":153722867}`, ""},
		{"cache ttl over max", head + `"cache_ttl_minutes":153722868}`, "cache_ttl_minutes"},
		{"min points negative", head + `"min_points":-1}`, "min_points"},
		{"min points at floor", head + `"min_points":0}`, ""},
		{"min points above floor", head + `"min_points":1}`, ""},
		{"dedup ttl negative", head + `"dedup_ttl_hours":-1}`, "dedup_ttl_hours"},
		{"dedup ttl at floor", head + `"dedup_ttl_hours":0}`, ""},
		{"dedup ttl above floor", head + `"dedup_ttl_hours":1}`, ""},
		// 2562047 hours is the largest value that survives Load's
		// dedup_ttl_hours*time.Hour multiplication the same way.
		{"dedup ttl at max", head + `"dedup_ttl_hours":2562047}`, ""},
		{"dedup ttl over max", head + `"dedup_ttl_hours":2562048}`, "dedup_ttl_hours"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadSettingsJSON(strings.NewReader(tc.body), curr)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ReadSettingsJSON: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q should name %q", err, tc.wantErr)
			}
		})
	}
}

// TestReadSettingsJSON_OutOfRangeCurrentSurvivesOmission keeps the new
// range checks on the payload only. A hand-edited config.toml can hold a
// cache_ttl_minutes of 1 — config.Validate clamps it at render time and
// warns — and that value arrives here through current. Rejecting the save
// over a field the caller never sent would blame the wrong edit and, worse,
// leave the user no way to fix anything else through --settings. Same rule
// pool_order already follows.
func TestReadSettingsJSON_OutOfRangeCurrentSurvivesOmission(t *testing.T) {
	bad := curr
	bad.CacheTTLMinutes = 1
	bad.MinPoints = -5
	bad.DedupTTLHours = -2
	got, err := ReadSettingsJSON(strings.NewReader(
		`{"topics":[],"style":"minimal","pools":["news","following"],"count":1}`,
	), bad)
	if err != nil {
		t.Fatalf("ReadSettingsJSON: %v", err)
	}
	if got.CacheTTLMinutes != 1 || got.MinPoints != -5 || got.DedupTTLHours != -2 {
		t.Errorf("omitted knobs = %d/%d/%d, want current's 1/-5/-2 untouched",
			got.CacheTTLMinutes, got.MinPoints, got.DedupTTLHours)
	}
}
