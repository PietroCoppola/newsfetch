package onboard

import (
	"bytes"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/PietroCoppola/newsfetch/internal/config"
)

func TestWriteConfig_CreatesFileAndParents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "newsfetch", "config.toml")
	if err := WriteConfig(path, Answers{Topics: []string{"rust", "go"}, Style: "boxed"}); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config not written: %v", err)
	}
}

func TestWriteConfig_RoundTripsThroughConfigLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	topics := []string{"rust", "databases"}
	if err := WriteConfig(path, Answers{Topics: topics, Style: "minimal"}); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if cfg.Style != "minimal" {
		t.Errorf("Style = %q, want %q", cfg.Style, "minimal")
	}
	gotTopics := append([]string(nil), cfg.Topics...)
	sort.Strings(gotTopics)
	wantTopics := append([]string(nil), topics...)
	sort.Strings(wantTopics)
	if !reflect.DeepEqual(gotTopics, wantTopics) {
		t.Errorf("Topics = %v, want %v", gotTopics, wantTopics)
	}
}

func TestWriteConfig_NoTopicsEmitsNoFilter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteConfig(path, Answers{Topics: nil, Style: "boxed"}); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if len(cfg.Topics) != 0 {
		t.Errorf("Topics = %v, want none", cfg.Topics)
	}
}

func TestWriteConfig_NilAggregatorsOmitsNewsTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteConfig(path, Answers{Topics: nil, Style: "boxed", NewsAggregators: nil}); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "[news]") || strings.Contains(string(data), "aggregators") {
		t.Errorf("file should not emit a [news] table when NewsAggregators is nil; got:\n%s", data)
	}
}

func TestWriteConfig_NonNilAggregatorsEmitsNewsTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteConfig(path, Answers{Topics: nil, Style: "boxed", NewsAggregators: []string{"hackernews", "lobsters"}}); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if !reflect.DeepEqual(cfg.News.Aggregators, []string{"hackernews", "lobsters"}) {
		t.Errorf("News.Aggregators = %v, want [hackernews lobsters]", cfg.News.Aggregators)
	}
}

func TestWriteConfig_RefusesToOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteConfig(path, Answers{Topics: []string{"rust"}, Style: "boxed"}); err != nil {
		t.Fatalf("first WriteConfig: %v", err)
	}
	err := WriteConfig(path, Answers{Topics: []string{"go"}, Style: "minimal"})
	if !errors.Is(err, ErrConfigExists) {
		t.Fatalf("err = %v, want ErrConfigExists", err)
	}
	// Verify original content was NOT overwritten.
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "rust") {
		t.Errorf("original content clobbered")
	}
	if strings.Contains(string(data), "minimal") {
		t.Errorf("second WriteConfig changed file content despite error")
	}
}

func TestOverwriteConfig_ReplacesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteConfig(path, Answers{Topics: []string{"rust"}, Style: "boxed"}); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	if err := OverwriteConfig(path, Answers{Topics: []string{"go"}, Style: "minimal", NewsAggregators: []string{"hackernews"}}); err != nil {
		t.Fatalf("OverwriteConfig: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if !reflect.DeepEqual(cfg.Topics, []string{"go"}) {
		t.Errorf("Topics = %v, want [go]", cfg.Topics)
	}
	if cfg.Style != "minimal" {
		t.Errorf("Style = %q, want minimal", cfg.Style)
	}
	if !reflect.DeepEqual(cfg.News.Aggregators, []string{"hackernews"}) {
		t.Errorf("News.Aggregators = %v, want [hackernews]", cfg.News.Aggregators)
	}
}

func TestWriteConfig_CountAndTickerRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	answers := Answers{
		Topics:          nil,
		Style:           "boxed",
		NewsAggregators: []string{"hackernews"},
		Count:           3,
		TickerMarker:    "branch",
		TickerBoxed:     true,
	}
	if err := WriteConfig(path, answers); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if cfg.Count != 3 {
		t.Errorf("Count = %d, want 3", cfg.Count)
	}
	if cfg.TickerMarker != "branch" {
		t.Errorf("TickerMarker = %q, want branch", cfg.TickerMarker)
	}
	if !cfg.TickerBoxed {
		t.Errorf("TickerBoxed = %v, want true", cfg.TickerBoxed)
	}
}

func TestWriteConfig_TickerFieldsEmittedEvenWhenInert(t *testing.T) {
	// User has style=minimal (ticker fields are inert) but their TickerMarker
	// is set to "branch" from a prior config. The writer must persist it so a
	// future switch back to style=boxed restores the prior tuning instead of
	// silently reverting to the default.
	path := filepath.Join(t.TempDir(), "config.toml")
	answers := Answers{
		Topics:          nil,
		Style:           "minimal",
		NewsAggregators: []string{"hackernews"},
		Count:           1,
		TickerMarker:    "branch",
		TickerBoxed:     true,
	}
	if err := WriteConfig(path, answers); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	data, _ := os.ReadFile(path)
	body := string(data)
	if !strings.Contains(body, "ticker_marker") || !strings.Contains(body, "branch") {
		t.Errorf("ticker_marker not persisted; got:\n%s", body)
	}
	if !strings.Contains(body, "ticker_boxed = true") {
		t.Errorf("ticker_boxed not persisted; got:\n%s", body)
	}
}

func TestOverwriteConfig_CreatesWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "newsubdir", "config.toml")
	if err := OverwriteConfig(path, Answers{Topics: nil, Style: "boxed", NewsAggregators: []string{"hackernews"}}); err != nil {
		t.Fatalf("OverwriteConfig: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config not written: %v", err)
	}
}

// intPtr and floatPtr build the optional per-feed knobs. The pointers are
// what carry "unset" through a --settings round trip, so tests that mean
// "the user set max_items to 2" have to say so with an address, not a value.
func intPtr(n int) *int { return &n }

func floatPtr(f float64) *float64 { return &f }

func TestRenderConfigTOML_Goldens(t *testing.T) {
	cases := []struct {
		name    string
		answers Answers
		want    string
	}{
		{
			name: "news only",
			answers: Answers{
				Topics:          []string{"rust", "go"},
				Style:           "boxed",
				Pools:           []string{"news"},
				NewsAggregators: []string{"hackernews"},
				Count:           1,
				FollowingCount:  1,
				TickerMarker:    "dot",
				TickerBoxed:     false,
				CacheTTLMinutes: 30,
				MinPoints:       50,
				DedupTTLHours:   6,
			},
			want: `# newsfetch config. Edit freely; see spec.md for field meanings.

topics = ["rust", "go"]
style = "boxed"
pools = ["news"]
count = 1
following_count = 1
ticker_marker = "dot"
ticker_boxed = false
cache_ttl_minutes = 30
min_points = 50
dedup_ttl_hours = 6

[news]
aggregators = ["hackernews"]
`,
		},
		{
			name: "news plus following with a bare feed",
			answers: Answers{
				Topics:          nil,
				Style:           "boxed",
				Pools:           []string{"news", "following"},
				PoolOrder:       []string{"following", "news"},
				NewsAggregators: []string{"hackernews"},
				Count:           2,
				FollowingCount:  1,
				Feeds:           []Feed{{URL: "https://drewdevault.com/blog/index.xml"}},
				TickerMarker:    "branch",
				TickerBoxed:     true,
				CacheTTLMinutes: 30,
				MinPoints:       50,
				DedupTTLHours:   6,
			},
			want: `# newsfetch config. Edit freely; see spec.md for field meanings.

topics = []
style = "boxed"
pools = ["news", "following"]
pool_order = ["following", "news"]
count = 2
following_count = 1
ticker_marker = "branch"
ticker_boxed = true
cache_ttl_minutes = 30
min_points = 50
dedup_ttl_hours = 6

[news]
aggregators = ["hackernews"]

[[following.feeds]]
url = "https://drewdevault.com/blog/index.xml"
`,
		},
		{
			name: "feed carrying both advanced knobs",
			answers: Answers{
				Topics:         []string{"ai"},
				Style:          "minimal",
				Pools:          []string{"following"},
				Count:          1,
				FollowingCount: 3,
				Feeds: []Feed{{
					URL:      "https://blog.cloudflare.com/rss/",
					MaxItems: intPtr(2),
					Weight:   floatPtr(0.3),
				}},
				TickerMarker:    "dot",
				TickerBoxed:     false,
				CacheTTLMinutes: 45,
				MinPoints:       10,
				DedupTTLHours:   3,
			},
			want: `# newsfetch config. Edit freely; see spec.md for field meanings.

topics = ["ai"]
style = "minimal"
pools = ["following"]
count = 1
following_count = 3
ticker_marker = "dot"
ticker_boxed = false
cache_ttl_minutes = 45
min_points = 10
dedup_ttl_hours = 3

[[following.feeds]]
url = "https://blog.cloudflare.com/rss/"
max_items = 2
weight = 0.3
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderConfigTOML(tc.answers)
			if got != tc.want {
				t.Errorf("renderConfigTOML mismatch\n--- got ---\n%s\n--- want ---\n%s", got, tc.want)
			}
		})
	}
}

func TestRenderConfigTOML_PoolOrderOmittedForSinglePool(t *testing.T) {
	// R-32: a single-pool config has nothing to order, and the wizard hides
	// the knob, so writing the key would put a setting in the file the user
	// was never asked about.
	got := renderConfigTOML(Answers{
		Style:           "boxed",
		Pools:           []string{"news"},
		PoolOrder:       []string{"following", "news"},
		NewsAggregators: []string{"hackernews"},
		Count:           1,
		FollowingCount:  1,
		TickerMarker:    "dot",
	})
	if strings.Contains(got, "pool_order") {
		t.Errorf("pool_order emitted for a one-pool config:\n%s", got)
	}
}

func TestTOMLFloat(t *testing.T) {
	// A whole-number weight must keep an explicit fractional part. Without
	// it the writer emits `weight = 5`, a TOML integer, and the config
	// loader is then relying on the decoder's int-to-float leniency rather
	// than on the file saying what it means.
	cases := []struct {
		in   float64
		want string
	}{
		{0.3, "0.3"},
		{1, "1.0"},
		{2, "2.0"},
		{5, "5.0"},
		{1.25, "1.25"},
		{0.05, "0.05"},
		// A non-finite value has no TOML literal at all. FormatFloat gives
		// "NaN" / "+Inf", neither of which contains ".", so the naive
		// fractional-part rule would emit `NaN.0` and corrupt the file for
		// the next config.Load. "" is the refusal; the caller omits the key.
		{math.NaN(), ""},
		{math.Inf(1), ""},
		{math.Inf(-1), ""},
	}
	for _, tc := range cases {
		if got := tomlFloat(tc.in); got != tc.want {
			t.Errorf("tomlFloat(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestRenderConfigTOML_NonFiniteWeightOmitsTheKey pins the call site, not
// just the helper: a refused literal must drop the whole `weight = ` line
// rather than emit an empty right-hand side, which would be just as
// unparseable as `NaN.0`.
func TestRenderConfigTOML_NonFiniteWeightOmitsTheKey(t *testing.T) {
	for _, w := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		got := renderConfigTOML(Answers{
			Style:           "boxed",
			Pools:           []string{"news"},
			NewsAggregators: []string{"hackernews"},
			Count:           1,
			FollowingCount:  1,
			TickerMarker:    "dot",
			Feeds:           []Feed{{URL: "https://a.example/f", Weight: floatPtr(w)}},
		})
		if strings.Contains(got, "weight") {
			t.Errorf("weight key emitted for %v:\n%s", w, got)
		}
		if !strings.Contains(got, `url = "https://a.example/f"`) {
			t.Errorf("the feed block itself must survive:\n%s", got)
		}
	}
}

func TestWriteConfig_PoolFieldsRoundTripThroughLoadAndValidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	answers := Answers{
		Topics:          []string{"rust"},
		Style:           "boxed",
		Pools:           []string{"news", "following"},
		PoolOrder:       []string{"following", "news"},
		NewsAggregators: []string{"hackernews", "lobsters"},
		Count:           2,
		FollowingCount:  3,
		Feeds: []Feed{
			{URL: "https://drewdevault.com/blog/index.xml"},
			{URL: "https://blog.cloudflare.com/rss/", MaxItems: intPtr(2), Weight: floatPtr(0.3)},
		},
		TickerMarker:    "branch",
		TickerBoxed:     true,
		CacheTTLMinutes: 30,
		MinPoints:       50,
		DedupTTLHours:   6,
	}
	if err := WriteConfig(path, answers); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	var buf bytes.Buffer
	cfg = config.Validate(cfg, config.FieldSources{Style: "config", Count: "config"}, &buf)
	if buf.Len() != 0 {
		t.Fatalf("written config produced validation warnings: %s", buf.String())
	}
	if !reflect.DeepEqual(cfg.Pools, []string{"news", "following"}) {
		t.Errorf("Pools = %v, want [news following]", cfg.Pools)
	}
	if !reflect.DeepEqual(cfg.PoolOrder, []string{"following", "news"}) {
		t.Errorf("PoolOrder = %v, want [following news]", cfg.PoolOrder)
	}
	if !reflect.DeepEqual(cfg.News.Aggregators, []string{"hackernews", "lobsters"}) {
		t.Errorf("News.Aggregators = %v, want [hackernews lobsters]", cfg.News.Aggregators)
	}
	if cfg.Count != 2 {
		t.Errorf("Count = %d, want 2", cfg.Count)
	}
	if cfg.FollowingCount != 3 {
		t.Errorf("FollowingCount = %d, want 3", cfg.FollowingCount)
	}
	want := []config.FeedConfig{
		{URL: "https://drewdevault.com/blog/index.xml"},
		{URL: "https://blog.cloudflare.com/rss/", MaxItems: 2, Weight: 0.3},
	}
	if !reflect.DeepEqual(cfg.Following.Feeds, want) {
		t.Errorf("Following.Feeds = %+v, want %+v", cfg.Following.Feeds, want)
	}
}

func TestWriteConfig_DisabledFollowingStillPersistsFeeds(t *testing.T) {
	// Persist-don't-clear: dropping "following" from pools must leave the
	// feed blocks on disk so re-enabling the pool restores them. This is the
	// case where a lossy writer is silent — the user sees no following box
	// either way, and only notices the loss weeks later when they re-enable.
	path := filepath.Join(t.TempDir(), "config.toml")
	answers := Answers{
		Topics:          []string{"rust"},
		Style:           "boxed",
		Pools:           []string{"news"},
		NewsAggregators: []string{"hackernews"},
		Count:           1,
		FollowingCount:  1,
		Feeds: []Feed{
			{URL: "https://blog.cloudflare.com/rss/", MaxItems: intPtr(2), Weight: floatPtr(0.3)},
		},
		TickerMarker:    "dot",
		CacheTTLMinutes: 30,
		MinPoints:       50,
		DedupTTLHours:   6,
	}
	if err := WriteConfig(path, answers); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "pool_order") {
		t.Errorf("pool_order emitted for a one-pool config:\n%s", data)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if !reflect.DeepEqual(cfg.Pools, []string{"news"}) {
		t.Errorf("Pools = %v, want [news]", cfg.Pools)
	}
	want := []config.FeedConfig{{URL: "https://blog.cloudflare.com/rss/", MaxItems: 2, Weight: 0.3}}
	if !reflect.DeepEqual(cfg.Following.Feeds, want) {
		t.Errorf("Following.Feeds = %+v, want %+v (feeds must survive a disabled pool)", cfg.Following.Feeds, want)
	}
	var buf bytes.Buffer
	if cfg = config.Validate(cfg, config.FieldSources{}, &buf); buf.Len() != 0 {
		t.Errorf("disabled-following config produced validation warnings: %s", buf.String())
	}
	if len(cfg.Following.Feeds) != 1 {
		t.Errorf("Validate dropped the feeds of a disabled pool: %+v", cfg.Following.Feeds)
	}
}
