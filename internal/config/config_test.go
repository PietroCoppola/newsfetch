package config_test

import (
	"bytes"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/config"
	"github.com/PietroCoppola/newsfetch/internal/defaults"
	"github.com/PietroCoppola/newsfetch/internal/fetch"
)

func TestDefaults(t *testing.T) {
	got := config.Defaults()
	if got.Style != defaults.Style {
		t.Errorf("Style = %q, want %q", got.Style, defaults.Style)
	}
	if got.CacheTTL != defaults.CacheTTL {
		t.Errorf("CacheTTL = %v, want %v", got.CacheTTL, defaults.CacheTTL)
	}
	if got.MinPoints != defaults.MinPoints {
		t.Errorf("MinPoints = %d, want %d", got.MinPoints, defaults.MinPoints)
	}
	if got.Topics != nil {
		t.Errorf("Topics = %v, want nil", got.Topics)
	}
}

func TestPath(t *testing.T) {
	dir := "/tmp/newsfetch-xdg"
	cases := []struct {
		name    string
		xdg     string
		wantSub string
		wantErr bool
	}{
		{"xdg absolute", dir, dir + "/newsfetch/config.toml", false},
		// "empty" and "unset" are the same code path because Path() uses
		// os.Getenv, which returns "" for both absent and empty-valued
		// variables. Both cases are listed to document intent; do not
		// collapse without also verifying Path() still uses os.Getenv.
		{"xdg empty falls back to home", "", ".config/newsfetch/config.toml", false},
		{"xdg unset falls back to home", "__UNSET__", ".config/newsfetch/config.toml", false},
		{"xdg not absolute falls back to home", "relative/path", ".config/newsfetch/config.toml", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.xdg == "__UNSET__" {
				t.Setenv("XDG_CONFIG_HOME", "")
				_ = os.Unsetenv("XDG_CONFIG_HOME")
			} else {
				t.Setenv("XDG_CONFIG_HOME", tc.xdg)
			}
			got, err := config.Path()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Path() = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Path: %v", err)
			}
			if !strings.HasSuffix(got, tc.wantSub) {
				t.Errorf("Path() = %q, want suffix %q", got, tc.wantSub)
			}
		})
	}
}

func TestLoad_Missing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.toml")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(cfg, config.Defaults()) {
		t.Errorf("Load = %+v, want Defaults() = %+v", cfg, config.Defaults())
	}
}

func TestLoad_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `
topics = ["rust", "ai"]
style = "minimal"
cache_ttl_minutes = 15
min_points = 100
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(cfg.Topics, []string{"rust", "ai"}) {
		t.Errorf("Topics = %v, want [rust ai]", cfg.Topics)
	}
	if cfg.Style != "minimal" {
		t.Errorf("Style = %q, want minimal", cfg.Style)
	}
	if cfg.CacheTTL != 15*time.Minute {
		t.Errorf("CacheTTL = %v, want 15m", cfg.CacheTTL)
	}
	if cfg.MinPoints != 100 {
		t.Errorf("MinPoints = %d, want 100", cfg.MinPoints)
	}
}

func TestLoad_Partial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(`style = "json"`+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Style != "json" {
		t.Errorf("Style = %q, want json", cfg.Style)
	}
	// Untouched fields keep defaults. Topics == nil is the sentinel that
	// drives the no-filter code path in rank.Score; pin it explicitly.
	if cfg.Topics != nil {
		t.Errorf("Topics = %v, want nil (not set in file)", cfg.Topics)
	}
	if cfg.CacheTTL != config.Defaults().CacheTTL {
		t.Errorf("CacheTTL = %v, want default %v", cfg.CacheTTL, config.Defaults().CacheTTL)
	}
	if cfg.MinPoints != config.Defaults().MinPoints {
		t.Errorf("MinPoints = %d, want default %d", cfg.MinPoints, config.Defaults().MinPoints)
	}
}

func TestLoad_UnknownKeysIgnored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `
style = "minimal"
dedupe_history = true
seen_history_capacity = 500
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load (unknown keys should be ignored silently): %v", err)
	}
	if cfg.Style != "minimal" {
		t.Errorf("Style = %q, want minimal", cfg.Style)
	}
}

func TestLoad_Sources(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"sources unset → defaults", "style = \"boxed\"\n", config.Defaults().News.Aggregators},
		{"sources set → used as-is", "sources = [\"hackernews\", \"lobsters\"]\n", []string{"hackernews", "lobsters"}},
		{"sources empty list survives Load (Validate handles)", "sources = []\n", []string{}},
		{"unknown name survives Load (Validate handles)", "sources = [\"weirdsrc\"]\n", []string{"weirdsrc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !reflect.DeepEqual(cfg.News.Aggregators, tc.want) {
				t.Errorf("News.Aggregators = %v, want %v", cfg.News.Aggregators, tc.want)
			}
		})
	}
}

func TestLoad_ParseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("style = 'boxed\nbroken"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := config.Load(path)
	if err == nil {
		t.Fatal("Load: want parse error, got nil")
	}
	if !reflect.DeepEqual(cfg, config.Defaults()) {
		t.Errorf("Load returned non-default cfg on parse error: %+v", cfg)
	}
}

func TestValidate_Clean(t *testing.T) {
	cfg := config.Defaults()
	var buf bytes.Buffer
	got := config.Validate(cfg, config.FieldSources{}, &buf)
	if !reflect.DeepEqual(got, cfg) {
		t.Errorf("Validate mutated clean config: %+v", got)
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected warning: %q", buf.String())
	}
}

func TestValidate_BadStyleFromConfig(t *testing.T) {
	cfg := config.Defaults()
	cfg.Style = "wat"
	var buf bytes.Buffer
	got := config.Validate(cfg, config.FieldSources{Style: "config"}, &buf)
	if got.Style != "boxed" {
		t.Errorf("Style = %q, want boxed", got.Style)
	}
	out := buf.String()
	if !strings.Contains(out, "unknown style") || !strings.Contains(out, "wat") || !strings.Contains(out, "from config") {
		t.Errorf("warning text missing details: %q", out)
	}
}

func TestValidate_StatuslineStyleIsValid(t *testing.T) {
	cfg := config.Defaults()
	cfg.Style = "statusline"
	var buf bytes.Buffer
	got := config.Validate(cfg, config.FieldSources{Style: "flag"}, &buf)
	if got.Style != "statusline" {
		t.Errorf("Style = %q, want statusline", got.Style)
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected warning: %s", buf.String())
	}
}

func TestValidate_StatuslineStyleFromConfigWarnsAndFallsBack(t *testing.T) {
	cfg := config.Defaults()
	cfg.Style = "statusline"
	var buf bytes.Buffer
	got := config.Validate(cfg, config.FieldSources{Style: "config"}, &buf)
	if got.Style != "boxed" {
		t.Errorf("Style = %q, want boxed", got.Style)
	}
	if !strings.Contains(buf.String(), "flag-only") {
		t.Errorf("expected flag-only warning, got %q", buf.String())
	}
}

func TestValidate_BadStyleFromFlag(t *testing.T) {
	cfg := config.Defaults()
	cfg.Style = "wat"
	var buf bytes.Buffer
	config.Validate(cfg, config.FieldSources{Style: "flag"}, &buf)
	if !strings.Contains(buf.String(), "from --style") {
		t.Errorf("warning should name --style as source: %q", buf.String())
	}
}

func TestValidate_TTLBelowFloor(t *testing.T) {
	cfg := config.Defaults()
	cfg.CacheTTL = 2 * time.Minute
	var buf bytes.Buffer
	got := config.Validate(cfg, config.FieldSources{}, &buf)
	if got.CacheTTL != 5*time.Minute {
		t.Errorf("CacheTTL = %v, want 5m", got.CacheTTL)
	}
	if !strings.Contains(buf.String(), "cache_ttl_minutes=2") {
		t.Errorf("warning missing ttl=2: %q", buf.String())
	}
}

func TestValidate_MinPointsNegative(t *testing.T) {
	cfg := config.Defaults()
	cfg.MinPoints = -4
	var buf bytes.Buffer
	got := config.Validate(cfg, config.FieldSources{}, &buf)
	if got.MinPoints != 0 {
		t.Errorf("MinPoints = %d, want 0", got.MinPoints)
	}
	if !strings.Contains(buf.String(), "min_points=-4") {
		t.Errorf("warning missing min_points=-4: %q", buf.String())
	}
}

// TestValidate_AggregatorsAllUnknownDropped covers both sides of the
// cascade split. With another pool still holding content, the aggregator
// warning fires and names what it dropped. With nothing else to render, the
// all-empty rule wins the single warning slot instead — deliberately, since
// its message describes the correction that actually happened rather than
// printing an empty list silentlyCorrect is about to overwrite.
func TestValidate_AggregatorsAllUnknownDropped(t *testing.T) {
	t.Run("following still has content", func(t *testing.T) {
		cfg := config.Defaults()
		cfg.Pools = []string{"news", "following"}
		cfg.News.Aggregators = []string{"weirdsrc", "another"}
		cfg.Following.Feeds = []config.FeedConfig{validFeed()}
		var buf bytes.Buffer
		got := config.Validate(cfg, config.FieldSources{}, &buf)
		if len(got.News.Aggregators) != 0 {
			t.Errorf("News.Aggregators = %v, want empty", got.News.Aggregators)
		}
		out := buf.String()
		if !strings.Contains(out, "weirdsrc") || !strings.Contains(out, "dropped") {
			t.Errorf("warning missing dropped names: %q", out)
		}
	})
	t.Run("nothing else to render", func(t *testing.T) {
		cfg := config.Defaults()
		cfg.News.Aggregators = []string{"weirdsrc", "another"}
		var buf bytes.Buffer
		got := config.Validate(cfg, config.FieldSources{}, &buf)
		if !reflect.DeepEqual(got.News.Aggregators, defaults.Sources()) {
			t.Errorf("News.Aggregators = %v, want %v", got.News.Aggregators, defaults.Sources())
		}
		if !strings.Contains(buf.String(), "produced no content") {
			t.Errorf("warning text missing: %q", buf.String())
		}
	})
}

func TestValidate_SourcesPartialUnknownDropped(t *testing.T) {
	cfg := config.Defaults()
	cfg.News.Aggregators = []string{"hackernews", "weirdsrc", "lobsters"}
	var buf bytes.Buffer
	got := config.Validate(cfg, config.FieldSources{}, &buf)
	if !reflect.DeepEqual(got.News.Aggregators, []string{"hackernews", "lobsters"}) {
		t.Errorf("News.Aggregators = %v, want [hackernews lobsters]", got.News.Aggregators)
	}
	out := buf.String()
	if !strings.Contains(out, "weirdsrc") || !strings.Contains(out, "dropped") {
		t.Errorf("warning missing dropped name: %q", out)
	}
}

func TestValidate_SourcesAllValidNoWarning(t *testing.T) {
	cfg := config.Defaults()
	cfg.News.Aggregators = []string{"hackernews", "lobsters"}
	var buf bytes.Buffer
	got := config.Validate(cfg, config.FieldSources{}, &buf)
	if !reflect.DeepEqual(got.News.Aggregators, []string{"hackernews", "lobsters"}) {
		t.Errorf("News.Aggregators mutated: %v", got.News.Aggregators)
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected warning: %q", buf.String())
	}
}

func TestValidate_CountTooLow(t *testing.T) {
	cfg := config.Defaults()
	cfg.Count = 0
	var buf bytes.Buffer
	got := config.Validate(cfg, config.FieldSources{Count: "config"}, &buf)
	if got.Count != 1 {
		t.Errorf("Count = %d, want 1 (clamped)", got.Count)
	}
	out := buf.String()
	if !strings.Contains(out, "count=0") || !strings.Contains(out, "from config") {
		t.Errorf("warning missing details: %q", out)
	}
}

func TestValidate_CountTooHigh(t *testing.T) {
	cfg := config.Defaults()
	cfg.Count = 99
	var buf bytes.Buffer
	got := config.Validate(cfg, config.FieldSources{Count: "flag"}, &buf)
	if got.Count != 4 {
		t.Errorf("Count = %d, want 4 (clamped)", got.Count)
	}
	if !strings.Contains(buf.String(), "from --count") {
		t.Errorf("warning should name --count as source: %q", buf.String())
	}
}

func TestValidate_UnknownTickerMarker(t *testing.T) {
	cfg := config.Defaults()
	cfg.TickerMarker = "spiral"
	var buf bytes.Buffer
	got := config.Validate(cfg, config.FieldSources{}, &buf)
	if got.TickerMarker != "dot" {
		t.Errorf("TickerMarker = %q, want dot (reset)", got.TickerMarker)
	}
	if !strings.Contains(buf.String(), "ticker_marker") || !strings.Contains(buf.String(), "spiral") {
		t.Errorf("warning missing details: %q", buf.String())
	}
}

func TestValidate_NegativeDedupWindow(t *testing.T) {
	cfg := config.Defaults()
	cfg.DedupWindow = -1 * time.Hour
	var buf bytes.Buffer
	got := config.Validate(cfg, config.FieldSources{}, &buf)
	if got.DedupWindow != 0 {
		t.Errorf("DedupWindow = %v, want 0 (clamped)", got.DedupWindow)
	}
	out := buf.String()
	if !strings.Contains(out, "dedup_ttl_hours") || !strings.Contains(out, "disabled") {
		t.Errorf("warning should explain dedup disabled; got %q", out)
	}
}

func TestValidate_BudgetStyleWinsOverTTL(t *testing.T) {
	cfg := config.Defaults()
	cfg.Style = "wat"
	cfg.CacheTTL = 1 * time.Minute
	var buf bytes.Buffer
	got := config.Validate(cfg, config.FieldSources{Style: "config"}, &buf)
	// Style gets warning and correction.
	if got.Style != "boxed" {
		t.Errorf("Style = %q, want boxed", got.Style)
	}
	// TTL is silently corrected (no second warning).
	if got.CacheTTL != 5*time.Minute {
		t.Errorf("CacheTTL = %v, want 5m (silently corrected)", got.CacheTTL)
	}
	// Exactly one line of warning output.
	lines := strings.Count(strings.TrimRight(buf.String(), "\n"), "\n") + 1
	if lines != 1 {
		t.Errorf("warning lines = %d, want 1; got %q", lines, buf.String())
	}
	if !strings.Contains(buf.String(), "unknown style") {
		t.Errorf("expected style to win precedence; got %q", buf.String())
	}
}

// writeConfig is the shared fixture helper for the Load tables: it drops a
// config body in a fresh temp dir and hands back the path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// TestLoad_PoolsAndLegacySources covers the whole read-time alias matrix.
// The legacy `sources` key is the M4 spelling of the news pool's
// aggregators and must keep working against config files that exist on
// disk today; `pools` present means the file speaks the new vocabulary and
// a leftover `sources` line is ignored outright.
func TestLoad_PoolsAndLegacySources(t *testing.T) {
	cases := []struct {
		name            string
		body            string
		wantPools       []string
		wantOrder       []string
		wantAggregators []string
	}{
		{
			name:            "neither pools nor sources",
			body:            "style = \"boxed\"\n",
			wantPools:       []string{"news"},
			wantOrder:       []string{"news"},
			wantAggregators: []string{"hackernews"},
		},
		{
			name:            "pools only, order filled from compile-time order",
			body:            "pools = [\"news\", \"following\"]\n",
			wantPools:       []string{"news", "following"},
			wantOrder:       []string{"following", "news"},
			wantAggregators: []string{"hackernews"},
		},
		{
			name:            "explicit pool_order wins over the compile-time order",
			body:            "pools = [\"news\", \"following\"]\npool_order = [\"news\", \"following\"]\n",
			wantPools:       []string{"news", "following"},
			wantOrder:       []string{"news", "following"},
			wantAggregators: []string{"hackernews"},
		},
		{
			name:            "legacy sources maps to the news pool",
			body:            "sources = [\"hackernews\", \"lobsters\"]\n",
			wantPools:       []string{"news"},
			wantOrder:       []string{"news"},
			wantAggregators: []string{"hackernews", "lobsters"},
		},
		{
			name:            "legacy sources empty list survives Load (Validate handles)",
			body:            "sources = []\n",
			wantPools:       []string{"news"},
			wantOrder:       []string{"news"},
			wantAggregators: []string{},
		},
		{
			name:            "unknown legacy name survives Load (Validate handles)",
			body:            "sources = [\"weirdsrc\"]\n",
			wantPools:       []string{"news"},
			wantOrder:       []string{"news"},
			wantAggregators: []string{"weirdsrc"},
		},
		{
			name:            "pools present means sources is ignored entirely",
			body:            "pools = [\"following\"]\nsources = [\"lobsters\"]\n",
			wantPools:       []string{"following"},
			wantOrder:       []string{"following", "news"},
			wantAggregators: []string{"hackernews"},
		},
		{
			name:            "aggregators beats sources when pools is absent",
			body:            "sources = [\"lobsters\"]\n\n[news]\naggregators = [\"hackernews\"]\n",
			wantPools:       []string{"news"},
			wantOrder:       []string{"news"},
			wantAggregators: []string{"hackernews"},
		},
		{
			name:            "aggregators alone leaves pools at defaults",
			body:            "[news]\naggregators = [\"lobsters\"]\n",
			wantPools:       []string{"news"},
			wantOrder:       []string{"news"},
			wantAggregators: []string{"lobsters"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(writeConfig(t, tc.body))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !reflect.DeepEqual(cfg.Pools, tc.wantPools) {
				t.Errorf("Pools = %v, want %v", cfg.Pools, tc.wantPools)
			}
			if !reflect.DeepEqual(cfg.PoolOrder, tc.wantOrder) {
				t.Errorf("PoolOrder = %v, want %v", cfg.PoolOrder, tc.wantOrder)
			}
			if !reflect.DeepEqual(cfg.News.Aggregators, tc.wantAggregators) {
				t.Errorf("News.Aggregators = %v, want %v", cfg.News.Aggregators, tc.wantAggregators)
			}
		})
	}
}

// TestLoad_FollowingFeeds pins the [[following.feeds]] decode, including
// the per-feed keys that are absent (which must land on 0, the "unset"
// spelling FeedConfig documents).
func TestLoad_FollowingFeeds(t *testing.T) {
	body := `
pools = ["news", "following"]
following_count = 3

[[following.feeds]]
url = "https://drewdevault.com/blog/index.xml"

[[following.feeds]]
url = "https://blog.cloudflare.com/rss/"
max_items = 2

[[following.feeds]]
url = "https://archived.example/feed.xml"
weight = 0.3
`
	cfg, err := config.Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []config.FeedConfig{
		{URL: "https://drewdevault.com/blog/index.xml", MaxItems: 0, Weight: 0},
		{URL: "https://blog.cloudflare.com/rss/", MaxItems: 2, Weight: 0},
		{URL: "https://archived.example/feed.xml", MaxItems: 0, Weight: 0.3},
	}
	if !reflect.DeepEqual(cfg.Following.Feeds, want) {
		t.Errorf("Following.Feeds = %+v, want %+v", cfg.Following.Feeds, want)
	}
	if cfg.FollowingCount != 3 {
		t.Errorf("FollowingCount = %d, want 3", cfg.FollowingCount)
	}
}

func TestLoad_NoFeedsLeavesFeedsNil(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, "pools = [\"following\"]\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Following.Feeds != nil {
		t.Errorf("Following.Feeds = %+v, want nil when the file declares none", cfg.Following.Feeds)
	}
	if cfg.FollowingCount != defaults.FollowingCount {
		t.Errorf("FollowingCount = %d, want default %d", cfg.FollowingCount, defaults.FollowingCount)
	}
}

// TestLoad_ExplicitZeroIsNotAbsent pins the reason the raw decode struct
// uses pointer fields for max_items and weight. FeedConfig reserves 0 for
// "unset", so a typed 0 that decoded straight through would be
// indistinguishable from an absent key and would slide into the default
// silently instead of earning the out-of-range warning Validate owes a
// user who typed a value outside [1, 10] / (0, 5].
func TestLoad_ExplicitZeroIsNotAbsent(t *testing.T) {
	body := `
[[following.feeds]]
url = "https://absent.example/feed.xml"

[[following.feeds]]
url = "https://zero.example/feed.xml"
max_items = 0
weight = 0
`
	cfg, err := config.Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Following.Feeds) != 2 {
		t.Fatalf("got %d feeds, want 2", len(cfg.Following.Feeds))
	}
	absent, typed := cfg.Following.Feeds[0], cfg.Following.Feeds[1]
	if absent.MaxItems != 0 || absent.Weight != 0 {
		t.Errorf("absent keys = {MaxItems:%d Weight:%v}, want both 0 (unset)", absent.MaxItems, absent.Weight)
	}
	if typed.MaxItems == 0 {
		t.Error("explicit max_items = 0 collapsed onto the unset spelling; Validate can no longer warn about it")
	}
	if typed.Weight == 0 {
		t.Error("explicit weight = 0 collapsed onto the unset spelling; Validate can no longer warn about it")
	}
	// Both must land outside their valid ranges so Validate treats them as
	// values the user typed badly rather than values it invented.
	if typed.MaxItems > 0 {
		t.Errorf("explicit max_items = 0 became %d, want a value outside [1, 10]", typed.MaxItems)
	}
	if typed.Weight > 0 {
		t.Errorf("explicit weight = 0 became %v, want a value outside (0, 5]", typed.Weight)
	}
}

func TestLoad_UnknownPoolKeysIgnored(t *testing.T) {
	body := `
style = "minimal"
pools_order = ["news"]

[news]
aggregators = ["hackernews"]
provider = "nonsense"

[[following.feeds]]
url = "https://a.example/feed.xml"
refresh_minutes = 12
`
	cfg, err := config.Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load (unknown keys should be ignored silently): %v", err)
	}
	if cfg.Style != "minimal" {
		t.Errorf("Style = %q, want minimal", cfg.Style)
	}
	if len(cfg.Following.Feeds) != 1 || cfg.Following.Feeds[0].URL != "https://a.example/feed.xml" {
		t.Errorf("Following.Feeds = %+v, want the one declared feed", cfg.Following.Feeds)
	}
}

func TestLoad_ParseErrorInFeedBlockReturnsDefaults(t *testing.T) {
	body := "[[following.feeds]]\nurl = \"https://a.example/feed.xml\n"
	cfg, err := config.Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("Load: want parse error, got nil")
	}
	if !reflect.DeepEqual(cfg, config.Defaults()) {
		t.Errorf("Load returned non-default cfg on parse error: %+v", cfg)
	}
}

// TestLoad_DoesNotRewriteFile pins addendum item 6: the legacy `sources`
// key is aliased at read time and never migrated on disk. A BurntSushi/toml
// round trip is lossy to comments, and a config file the user hand-wrote is
// not ours to reformat behind their back.
func TestLoad_DoesNotRewriteFile(t *testing.T) {
	body := "# keep my comments and my spacing\n\nsources = [ \"hackernews\" ]\n"
	path := writeConfig(t, body)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	if _, err := config.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("Load rewrote the config file.\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestFeedURLs(t *testing.T) {
	cases := []struct {
		name  string
		feeds []config.FeedConfig
		want  []string
	}{
		{"no feeds", nil, nil},
		{
			"config order is preserved",
			[]config.FeedConfig{
				{URL: "https://b.example/feed.xml"},
				{URL: "https://a.example/feed.xml"},
			},
			[]string{"https://b.example/feed.xml", "https://a.example/feed.xml"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Following.Feeds = tc.feeds
			if got := cfg.FeedURLs(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("FeedURLs() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFeedSpecs(t *testing.T) {
	cfg := config.Defaults()
	cfg.Following.Feeds = []config.FeedConfig{
		{URL: "https://a.example/feed.xml"},
		{URL: "https://b.example/feed.xml", MaxItems: 2, Weight: 1.5},
	}
	want := []fetch.FeedSpec{
		{URL: "https://a.example/feed.xml", MaxItems: 0},
		{URL: "https://b.example/feed.xml", MaxItems: 2},
	}
	if got := cfg.FeedSpecs(); !reflect.DeepEqual(got, want) {
		t.Errorf("FeedSpecs() = %+v, want %+v", got, want)
	}
	if got := config.Defaults().FeedSpecs(); got != nil {
		t.Errorf("FeedSpecs() with no feeds = %+v, want nil", got)
	}
}

func TestPoolEnabledAndPoolActivity(t *testing.T) {
	feed := []config.FeedConfig{{URL: "https://a.example/feed.xml"}}
	aggs := []string{"hackernews"}
	cases := []struct {
		name                string
		pools               []string
		aggregators         []string
		feeds               []config.FeedConfig
		wantNews            bool
		wantFollowing       bool
		wantNewsActive      bool
		wantFollowingActive bool
	}{
		{"defaults: news only", []string{"news"}, aggs, nil, true, false, true, false},
		{"following enabled but no feeds", []string{"news", "following"}, aggs, nil, true, true, true, false},
		{"following enabled with feeds", []string{"news", "following"}, aggs, feed, true, true, true, true},
		{"feeds configured but pool disabled", []string{"news"}, aggs, feed, true, false, true, false},
		{"no pools at all", nil, aggs, feed, false, false, false, false},
		// R-8 makes this legal, and R-35 is the reason it has its own row:
		// news is enabled, so PoolEnabled("news") is true, but the pool can
		// produce nothing. Every caller must ask NewsActive, not PoolEnabled.
		{"news enabled but no aggregators", []string{"news"}, nil, nil, true, false, false, false},
		{"news enabled, empty aggregators, following active", []string{"news", "following"}, []string{}, feed, true, true, false, true},
		{"news disabled but aggregators configured", []string{"following"}, aggs, feed, false, true, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Pools = tc.pools
			cfg.News.Aggregators = tc.aggregators
			cfg.Following.Feeds = tc.feeds
			if got := cfg.PoolEnabled("news"); got != tc.wantNews {
				t.Errorf("PoolEnabled(news) = %v, want %v", got, tc.wantNews)
			}
			if got := cfg.PoolEnabled("following"); got != tc.wantFollowing {
				t.Errorf("PoolEnabled(following) = %v, want %v", got, tc.wantFollowing)
			}
			if got := cfg.NewsActive(); got != tc.wantNewsActive {
				t.Errorf("NewsActive() = %v, want %v", got, tc.wantNewsActive)
			}
			if got := cfg.FollowingActive(); got != tc.wantFollowingActive {
				t.Errorf("FollowingActive() = %v, want %v", got, tc.wantFollowingActive)
			}
			if cfg.PoolEnabled("repos") {
				t.Error("PoolEnabled(repos) = true, want false for a name no config lists")
			}
		})
	}
}

func TestDefaults_PoolLayer(t *testing.T) {
	got := config.Defaults()
	if !reflect.DeepEqual(got.Pools, defaults.Pools()) {
		t.Errorf("Pools = %v, want %v", got.Pools, defaults.Pools())
	}
	// PoolOrder defaults to the ENABLED pools in compile-time order, not to
	// defaults.PoolOrder() itself: Validate normalises pool_order into a
	// permutation of the enabled pools, and Defaults() has to be a fixed
	// point of Validate (TestValidate_Clean pins that with DeepEqual).
	if !reflect.DeepEqual(got.PoolOrder, []string{"news"}) {
		t.Errorf("PoolOrder = %v, want [news]", got.PoolOrder)
	}
	if !reflect.DeepEqual(got.News.Aggregators, defaults.Sources()) {
		t.Errorf("News.Aggregators = %v, want %v", got.News.Aggregators, defaults.Sources())
	}
	if got.Following.Feeds != nil {
		t.Errorf("Following.Feeds = %+v, want nil", got.Following.Feeds)
	}
	if got.FollowingCount != defaults.FollowingCount {
		t.Errorf("FollowingCount = %d, want %d", got.FollowingCount, defaults.FollowingCount)
	}
}

// validFeed is the fixture feed used wherever a test needs the following
// pool to have content without caring what that content is.
func validFeed() config.FeedConfig {
	return config.FeedConfig{URL: "https://a.example/feed.xml"}
}

func TestValidate_UnknownPoolDropped(t *testing.T) {
	cfg := config.Defaults()
	cfg.Pools = []string{"news", "repos"}
	var buf bytes.Buffer
	got := config.Validate(cfg, config.FieldSources{}, &buf)
	if !reflect.DeepEqual(got.Pools, []string{"news"}) {
		t.Errorf("Pools = %v, want [news]", got.Pools)
	}
	out := buf.String()
	if !strings.Contains(out, "repos") || !strings.Contains(out, "dropped") {
		t.Errorf("warning missing the dropped name: %q", out)
	}
}

func TestValidate_DuplicatePoolsCollapsed(t *testing.T) {
	cfg := config.Defaults()
	cfg.Pools = []string{"news", "news"}
	var buf bytes.Buffer
	got := config.Validate(cfg, config.FieldSources{}, &buf)
	if !reflect.DeepEqual(got.Pools, []string{"news"}) {
		t.Errorf("Pools = %v, want [news]; a pool listed twice would render twice", got.Pools)
	}
	if buf.Len() != 0 {
		t.Errorf("collapsing a duplicate should be silent; got %q", buf.String())
	}
}

func TestValidate_PoolsEmptyFallsBack(t *testing.T) {
	cfg := config.Defaults()
	cfg.Pools = []string{}
	var buf bytes.Buffer
	got := config.Validate(cfg, config.FieldSources{}, &buf)
	if !reflect.DeepEqual(got.Pools, defaults.Pools()) {
		t.Errorf("Pools = %v, want %v", got.Pools, defaults.Pools())
	}
	if !strings.Contains(buf.String(), "pools is empty") {
		t.Errorf("warning text missing: %q", buf.String())
	}
}

func TestValidate_PoolsAllUnknownFallsBack(t *testing.T) {
	cfg := config.Defaults()
	cfg.Pools = []string{"repos", "weather"}
	var buf bytes.Buffer
	got := config.Validate(cfg, config.FieldSources{}, &buf)
	if !reflect.DeepEqual(got.Pools, defaults.Pools()) {
		t.Errorf("Pools = %v, want %v", got.Pools, defaults.Pools())
	}
	out := buf.String()
	if !strings.Contains(out, "no recognised names") || !strings.Contains(out, "repos") {
		t.Errorf("warning missing dropped names: %q", out)
	}
}

// TestValidate_PoolOrderNormalised pins the silent permutation rule. Every
// case must produce a PoolOrder that is exactly the enabled set, in a
// deterministic order, with no warning: a user who enables a pool and
// forgets to name it in pool_order has not made a mistake worth a line of
// stderr on every terminal open.
func TestValidate_PoolOrderNormalised(t *testing.T) {
	cases := []struct {
		name  string
		pools []string
		order []string
		want  []string
	}{
		{"already a permutation", []string{"news", "following"}, []string{"news", "following"}, []string{"news", "following"}},
		{"reordered by the user", []string{"news", "following"}, []string{"following", "news"}, []string{"following", "news"}},
		{"missing pool appended", []string{"news", "following"}, []string{"news"}, []string{"news", "following"}},
		{"empty order falls back to compile-time order", []string{"news", "following"}, nil, []string{"following", "news"}},
		{"unknown name dropped", []string{"news", "following"}, []string{"repos", "following", "news"}, []string{"following", "news"}},
		{"disabled pool dropped", []string{"news"}, []string{"following", "news"}, []string{"news"}},
		{"duplicates collapsed", []string{"news", "following"}, []string{"news", "news", "following"}, []string{"news", "following"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Pools = tc.pools
			cfg.PoolOrder = tc.order
			var buf bytes.Buffer
			got := config.Validate(cfg, config.FieldSources{}, &buf)
			if !reflect.DeepEqual(got.PoolOrder, tc.want) {
				t.Errorf("PoolOrder = %v, want %v", got.PoolOrder, tc.want)
			}
			if buf.Len() != 0 {
				t.Errorf("pool_order normalisation must be silent; got %q", buf.String())
			}
		})
	}
}

// TestValidate_FollowingIsNotAnAggregator is the negative-requirement
// regression test for the registry split. `following` is a pool, not an
// aggregator: it must stay unspellable in [news] aggregators, and
// cmd/newsfetch's newSource factory must stay without a "following" case.
func TestValidate_FollowingIsNotAnAggregator(t *testing.T) {
	for _, name := range fetch.KnownSourceNames() {
		if name == "following" {
			t.Fatal("fetch.KnownSourceNames lists \"following\"; the following pool must never be an aggregator")
		}
	}
	cfg := config.Defaults()
	cfg.Pools = []string{"news", "following"}
	cfg.News.Aggregators = []string{"following"}
	cfg.Following.Feeds = []config.FeedConfig{validFeed()}
	var buf bytes.Buffer
	got := config.Validate(cfg, config.FieldSources{}, &buf)
	for _, a := range got.News.Aggregators {
		if a == "following" {
			t.Errorf("News.Aggregators = %v, want \"following\" dropped", got.News.Aggregators)
		}
	}
	out := buf.String()
	if !strings.Contains(out, "following") || !strings.Contains(out, "dropped") {
		t.Errorf("warning should name the dropped aggregator: %q", out)
	}
}

// TestValidate_AggregatorsEmptyIsSilent pins the rule that an empty
// aggregator list is allowed: the news pool simply renders nothing, exactly
// like any other pool with an empty internal config. It is the all-empty
// rule below, not this one, that catches a config left with nothing to show.
func TestValidate_AggregatorsEmptyIsSilent(t *testing.T) {
	cfg := config.Defaults()
	cfg.Pools = []string{"news", "following"}
	cfg.News.Aggregators = []string{}
	cfg.Following.Feeds = []config.FeedConfig{validFeed()}
	var buf bytes.Buffer
	got := config.Validate(cfg, config.FieldSources{}, &buf)
	if len(got.News.Aggregators) != 0 {
		t.Errorf("News.Aggregators = %v, want left empty", got.News.Aggregators)
	}
	if buf.Len() != 0 {
		t.Errorf("an empty aggregator list must be silent; got %q", buf.String())
	}
}

// TestValidate_AllPoolsEmptyRestoresBoth pins that the fallback repairs
// BOTH halves. Resetting pools alone is a no-op when news is already
// enabled with zero aggregators — the warning would announce a fix that
// changed nothing.
func TestValidate_AllPoolsEmptyRestoresBoth(t *testing.T) {
	cases := []struct {
		name  string
		pools []string
		aggs  []string
	}{
		{"news only, no aggregators", []string{"news"}, []string{}},
		{"both pools, neither has content", []string{"news", "following"}, []string{}},
		{"following only, no feeds", []string{"following"}, []string{"hackernews"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Pools = tc.pools
			cfg.News.Aggregators = tc.aggs
			cfg.Following.Feeds = nil
			var buf bytes.Buffer
			got := config.Validate(cfg, config.FieldSources{}, &buf)
			if !reflect.DeepEqual(got.Pools, defaults.Pools()) {
				t.Errorf("Pools = %v, want %v", got.Pools, defaults.Pools())
			}
			if !reflect.DeepEqual(got.News.Aggregators, defaults.Sources()) {
				t.Errorf("News.Aggregators = %v, want %v", got.News.Aggregators, defaults.Sources())
			}
			if !strings.Contains(buf.String(), "produced no content") {
				t.Errorf("warning text missing: %q", buf.String())
			}
		})
	}
}

func TestValidate_FollowingCountClamped(t *testing.T) {
	cases := []struct {
		name     string
		in       int
		want     int
		wantWarn bool
	}{
		{"zero clamps up", 0, 1, true},
		{"negative clamps up", -3, 1, true},
		{"one is fine", 1, 1, false},
		{"max is fine", defaults.MaxCount, defaults.MaxCount, false},
		{"above max clamps down", defaults.MaxCount + 1, defaults.MaxCount, true},
		{"absurd clamps down", 99, defaults.MaxCount, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.FollowingCount = tc.in
			var buf bytes.Buffer
			got := config.Validate(cfg, config.FieldSources{}, &buf)
			if got.FollowingCount != tc.want {
				t.Errorf("FollowingCount = %d, want %d", got.FollowingCount, tc.want)
			}
			warned := strings.Contains(buf.String(), "following_count")
			if warned != tc.wantWarn {
				t.Errorf("warned = %v, want %v; output %q", warned, tc.wantWarn, buf.String())
			}
		})
	}
}

// TestValidate_FeedURLShapes pins which feeds survive. The check is syntax
// only — a reachability probe on the render hot path is not on the table —
// so anything that is not an absolute http/https URL with a host is dropped
// rather than handed to the fetcher to fail slowly.
func TestValidate_FeedURLShapes(t *testing.T) {
	cases := []struct {
		name string
		url  string
		keep bool
	}{
		{"https feed", "https://a.example/feed.xml", true},
		{"http feed", "http://a.example/feed.xml", true},
		{"empty", "", false},
		{"whitespace only", "   ", false},
		{"relative", "example.com/feed.xml", false},
		{"protocol relative", "//example.com/feed.xml", false},
		{"ftp scheme", "ftp://example.com/feed.xml", false},
		{"no host", "http://", false},
		{"unparseable escape", "http://%zz", false},
		{"unparseable host", "http://[::1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Pools = []string{"news", "following"}
			cfg.Following.Feeds = []config.FeedConfig{{URL: tc.url}}
			var buf bytes.Buffer
			got := config.Validate(cfg, config.FieldSources{}, &buf)
			if tc.keep {
				if len(got.Following.Feeds) != 1 {
					t.Fatalf("feed %q was dropped; want kept. warning: %q", tc.url, buf.String())
				}
				if buf.Len() != 0 {
					t.Errorf("a good feed must be silent; got %q", buf.String())
				}
				return
			}
			if len(got.Following.Feeds) != 0 {
				t.Fatalf("feed %q survived; want dropped", tc.url)
			}
			if !strings.Contains(buf.String(), "dropped") {
				t.Errorf("warning should say the feed was dropped; got %q", buf.String())
			}
		})
	}
}

// TestValidate_FeedKnobBoundaries walks every boundary of the two per-feed
// knobs. Zero is the "unset" spelling for both and must stay silent; the
// out-of-range values Load marks (an explicitly typed 0 becomes a negative)
// must be corrected and counted.
func TestValidate_FeedKnobBoundaries(t *testing.T) {
	cases := []struct {
		name       string
		in         config.FeedConfig
		wantItems  int
		wantWeight float64
		wantWarn   bool
	}{
		{"both unset", config.FeedConfig{URL: "https://a.example/f.xml"}, 0, 0, false},
		{"max_items at min", config.FeedConfig{URL: "https://a.example/f.xml", MaxItems: 1}, 1, 0, false},
		{"max_items at max", config.FeedConfig{URL: "https://a.example/f.xml", MaxItems: 10}, 10, 0, false},
		{"max_items above max", config.FeedConfig{URL: "https://a.example/f.xml", MaxItems: 11}, 10, 0, true},
		{"max_items typed zero", config.FeedConfig{URL: "https://a.example/f.xml", MaxItems: -1}, 1, 0, true},
		{"max_items negative", config.FeedConfig{URL: "https://a.example/f.xml", MaxItems: -7}, 1, 0, true},
		{"weight just above zero", config.FeedConfig{URL: "https://a.example/f.xml", Weight: 0.1}, 0, 0.1, false},
		{"weight at max", config.FeedConfig{URL: "https://a.example/f.xml", Weight: 5.0}, 0, 5.0, false},
		{"weight above max", config.FeedConfig{URL: "https://a.example/f.xml", Weight: 5.1}, 0, 5.0, true},
		{"weight typed zero", config.FeedConfig{URL: "https://a.example/f.xml", Weight: -1}, 0, 0, true},
		{"weight negative", config.FeedConfig{URL: "https://a.example/f.xml", Weight: -0.1}, 0, 0, true},
		// TOML spells these `nan` / `inf` / `-inf` and BurntSushi/toml
		// decodes all three into a float64 without an error, so they reach
		// Validate from a real config file. Every comparison against NaN is
		// false, so a range check alone lets NaN through untouched and the
		// settings writer then emits it back as an invalid literal.
		{"weight NaN", config.FeedConfig{URL: "https://a.example/f.xml", Weight: math.NaN()}, 0, 0, true},
		{"weight +Inf", config.FeedConfig{URL: "https://a.example/f.xml", Weight: math.Inf(1)}, 0, 0, true},
		{"weight -Inf", config.FeedConfig{URL: "https://a.example/f.xml", Weight: math.Inf(-1)}, 0, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Pools = []string{"news", "following"}
			cfg.Following.Feeds = []config.FeedConfig{tc.in}
			var buf bytes.Buffer
			got := config.Validate(cfg, config.FieldSources{}, &buf)
			if len(got.Following.Feeds) != 1 {
				t.Fatalf("feed was dropped; want kept and clamped. warning: %q", buf.String())
			}
			f := got.Following.Feeds[0]
			if f.MaxItems != tc.wantItems {
				t.Errorf("MaxItems = %d, want %d", f.MaxItems, tc.wantItems)
			}
			if f.Weight != tc.wantWeight {
				t.Errorf("Weight = %v, want %v", f.Weight, tc.wantWeight)
			}
			warned := buf.Len() != 0
			if warned != tc.wantWarn {
				t.Errorf("warned = %v, want %v; output %q", warned, tc.wantWarn, buf.String())
			}
		})
	}
}

// TestValidate_FeedWarningsAggregate pins the single-warning budget across
// many bad feeds: one line, counts rather than a list of URLs, and each
// distinct reason named once.
func TestValidate_FeedWarningsAggregate(t *testing.T) {
	cfg := config.Defaults()
	cfg.Pools = []string{"news", "following"}
	cfg.Following.Feeds = []config.FeedConfig{
		{URL: "example.com/one.xml"},
		{URL: "example.com/two.xml"},
		{URL: "https://good.example/f.xml", MaxItems: 99},
		{URL: "https://also.example/f.xml"},
	}
	var buf bytes.Buffer
	got := config.Validate(cfg, config.FieldSources{}, &buf)
	if len(got.Following.Feeds) != 2 {
		t.Fatalf("kept %d feeds, want 2", len(got.Following.Feeds))
	}
	out := buf.String()
	lines := strings.Count(strings.TrimRight(out, "\n"), "\n") + 1
	if lines != 1 {
		t.Errorf("warning lines = %d, want 1; got %q", lines, out)
	}
	if !strings.Contains(out, "2 feeds dropped") {
		t.Errorf("warning should count the drops; got %q", out)
	}
	if !strings.Contains(out, "1 feed adjusted") {
		t.Errorf("warning should count the clamps; got %q", out)
	}
	if strings.Count(out, "invalid url") != 1 {
		t.Errorf("a repeated reason should be named once; got %q", out)
	}
}

// TestValidate_FeedWarningIsLast pins the cascade position: a malformed feed
// must never mask a warning the user is more likely to act on, and the feed
// correction still happens either way.
func TestValidate_FeedWarningIsLast(t *testing.T) {
	base := func() config.Config {
		cfg := config.Defaults()
		cfg.Pools = []string{"news", "following"}
		cfg.Following.Feeds = []config.FeedConfig{{URL: "not a url"}}
		return cfg
	}
	cases := []struct {
		name    string
		mutate  func(*config.Config)
		src     config.FieldSources
		wantSub string
	}{
		{"style wins", func(c *config.Config) { c.Style = "wat" }, config.FieldSources{Style: "config"}, "unknown style"},
		{"ttl wins", func(c *config.Config) { c.CacheTTL = time.Minute }, config.FieldSources{}, "cache_ttl_minutes"},
		{"count wins", func(c *config.Config) { c.Count = 99 }, config.FieldSources{Count: "config"}, "count=99"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.mutate(&cfg)
			var buf bytes.Buffer
			got := config.Validate(cfg, tc.src, &buf)
			out := buf.String()
			lines := strings.Count(strings.TrimRight(out, "\n"), "\n") + 1
			if lines != 1 {
				t.Errorf("warning lines = %d, want 1; got %q", lines, out)
			}
			if !strings.Contains(out, tc.wantSub) {
				t.Errorf("expected %q to win precedence; got %q", tc.wantSub, out)
			}
			if len(got.Following.Feeds) != 0 {
				t.Errorf("the bad feed survived silent correction: %+v", got.Following.Feeds)
			}
		})
	}
}

// TestValidate_FeedsCheckedWhileFollowingDisabled pins that feed rules do
// not depend on the pool being enabled. One code path, and the warning is
// about the config file — which is what the user edits, whether or not they
// have turned the pool on yet.
func TestValidate_FeedsCheckedWhileFollowingDisabled(t *testing.T) {
	cfg := config.Defaults()
	cfg.Pools = []string{"news"}
	cfg.Following.Feeds = []config.FeedConfig{
		{URL: "ftp://example.com/f.xml"},
		{URL: "https://good.example/f.xml", MaxItems: 42},
	}
	var buf bytes.Buffer
	got := config.Validate(cfg, config.FieldSources{}, &buf)
	if len(got.Following.Feeds) != 1 {
		t.Fatalf("kept %d feeds, want 1", len(got.Following.Feeds))
	}
	if got.Following.Feeds[0].MaxItems != 10 {
		t.Errorf("MaxItems = %d, want 10 (clamped even with the pool off)", got.Following.Feeds[0].MaxItems)
	}
	if !strings.Contains(buf.String(), "1 feed dropped") {
		t.Errorf("warning missing: %q", buf.String())
	}
}

// TestValidate_FeedSpecsAfterValidate closes the loop between validation and
// the fetch layer: what FeedSpecs hands the fan-out is the corrected list,
// never the raw one.
func TestValidate_FeedSpecsAfterValidate(t *testing.T) {
	cfg := config.Defaults()
	cfg.Pools = []string{"news", "following"}
	cfg.Following.Feeds = []config.FeedConfig{
		{URL: "nonsense"},
		{URL: "https://good.example/f.xml", MaxItems: 99},
	}
	got := config.Validate(cfg, config.FieldSources{}, io.Discard)
	want := []fetch.FeedSpec{{URL: "https://good.example/f.xml", MaxItems: 10}}
	if !reflect.DeepEqual(got.FeedSpecs(), want) {
		t.Errorf("FeedSpecs() = %+v, want %+v", got.FeedSpecs(), want)
	}
	if !reflect.DeepEqual(got.FeedURLs(), []string{"https://good.example/f.xml"}) {
		t.Errorf("FeedURLs() = %v, want the surviving feed only", got.FeedURLs())
	}
}

// TestValidate_Idempotent pins the property the statusline path depends on:
// it validates a second time against io.Discard, so a Validate that kept
// changing its own output would make the two renders disagree. pool_order's
// permutation normalisation is the non-trivial part — appending the pools a
// user left out must not append them again on the second pass.
func TestValidate_Idempotent(t *testing.T) {
	cases := []struct {
		name  string
		build func() config.Config
	}{
		{"defaults", config.Defaults},
		{"unknown pool and a scrambled order", func() config.Config {
			c := config.Defaults()
			c.Pools = []string{"following", "repos", "news"}
			c.PoolOrder = []string{"repos", "news"}
			c.Following.Feeds = []config.FeedConfig{validFeed()}
			return c
		}},
		{"missing pool appended to the order", func() config.Config {
			c := config.Defaults()
			c.Pools = []string{"news", "following"}
			c.PoolOrder = []string{"news"}
			c.Following.Feeds = []config.FeedConfig{validFeed()}
			return c
		}},
		{"everything empty", func() config.Config {
			c := config.Defaults()
			c.Pools = nil
			c.PoolOrder = nil
			c.News.Aggregators = nil
			return c
		}},
		{"bad feeds and bad knobs", func() config.Config {
			c := config.Defaults()
			c.Pools = []string{"news", "following"}
			c.PoolOrder = []string{"following"}
			c.Following.Feeds = []config.FeedConfig{
				{URL: "nope"},
				{URL: "https://a.example/f.xml", MaxItems: 99, Weight: 9},
			}
			c.FollowingCount = 0
			c.Count = 99
			c.CacheTTL = time.Minute
			c.DedupWindow = -time.Hour
			c.TickerMarker = "spiral"
			return c
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var first bytes.Buffer
			once := config.Validate(tc.build(), config.FieldSources{}, &first)
			var second bytes.Buffer
			twice := config.Validate(once, config.FieldSources{}, &second)
			if !reflect.DeepEqual(twice, once) {
				t.Errorf("Validate is not idempotent:\nonce:  %+v\ntwice: %+v", once, twice)
			}
			if second.Len() != 0 {
				t.Errorf("second pass warned about an already-corrected config: %q", second.String())
			}
		})
	}
}
