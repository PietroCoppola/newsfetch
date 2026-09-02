package render_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/fetch"
	"github.com/PietroCoppola/newsfetch/internal/render"
)

// poolFixture returns three deterministic stories for the pool-stacking
// tests. Times are fixed so every snapshot is stable.
func poolFixture(now time.Time) []fetch.Story {
	return []fetch.Story{
		{
			Title:     "The case for slow blogging",
			URL:       "https://drewdevault.com/2026/slow",
			Source:    "following",
			Author:    "drew",
			CreatedAt: now.Add(-14 * 24 * time.Hour),
		},
		{
			Title:     "Show HN: a tiny Go CLI for terminal news",
			URL:       "https://github.com/example/newsfetch",
			Source:    "hackernews",
			Author:    "alice",
			CreatedAt: now.Add(-4 * time.Hour),
		},
		{
			Title:     "Third story",
			URL:       "https://example.org/news",
			Source:    "lobsters",
			CreatedAt: now.Add(-30 * time.Minute),
		},
	}
}

// mustPools unwraps Pools for tests whose inputs are valid by construction.
func mustPools(t *testing.T, pools []render.Pool, now time.Time, width int, opts render.MultiOptions) string {
	t.Helper()
	got, err := render.Pools(pools, now, width, opts)
	if err != nil {
		t.Fatalf("Pools: %v", err)
	}
	return got
}

// mustMultiForPools unwraps Multi so pool tests can state their expectation
// as "exactly what the one-pool renderer produces".
func mustMultiForPools(t *testing.T, stories []fetch.Story, now time.Time, width int, opts render.MultiOptions) string {
	t.Helper()
	got, err := render.Multi(stories, now, width, opts)
	if err != nil {
		t.Fatalf("Multi: %v", err)
	}
	return got
}

// TestPools_SinglePoolIsByteIdenticalToMulti is the primary regression
// guard for the whole milestone: a user with one pool must get exactly the
// bytes the pre-pool renderer produced, header included (there is none),
// across every count and ticker style.
func TestPools_SinglePoolIsByteIdenticalToMulti(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	stories := poolFixture(now)
	cases := []struct {
		name string
		n    int
		opts render.MultiOptions
	}{
		{"count 1 plain", 1, render.MultiOptions{Marker: render.TickerDot}},
		{"count 1 boxed", 1, render.MultiOptions{Marker: render.TickerDot, Boxed: true}},
		{"count 3 plain", 3, render.MultiOptions{Marker: render.TickerDot}},
		{"count 3 boxed", 3, render.MultiOptions{Marker: render.TickerDot, Boxed: true}},
		{"count 3 branch plain", 3, render.MultiOptions{Marker: render.TickerBranch}},
		{"count 3 branch boxed", 3, render.MultiOptions{Marker: render.TickerBranch, Boxed: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sel := stories[:tc.n]
			// A caller may hand Pools a Label even for a lone pool; the
			// rule is that headers follow what RENDERS, so it must not
			// appear.
			got := mustPools(t, []render.Pool{
				{Name: "following", Label: "Following", Stories: sel},
			}, now, 50, tc.opts)
			want := mustMultiForPools(t, sel, now, 50, tc.opts)
			if got != want {
				t.Errorf("single-pool Pools drifted from Multi\n--- got ---\n%s--- want ---\n%s", got, want)
			}
		})
	}
}

// TestPools_TwoPoolsStackFlushWithHeaders pins ruling R-17 (labels appear
// once two pools render) and R-18 (no blank separator between boxes) as a
// byte-exact golden.
func TestPools_TwoPoolsStackFlushWithHeaders(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	stories := poolFixture(now)
	got := mustPools(t, []render.Pool{
		{Name: "following", Label: "Following", Stories: stories[0:1]},
		{Name: "news", Label: "News", Stories: stories[1:2]},
	}, now, 50, render.MultiOptions{Marker: render.TickerDot})
	want := "" +
		"╭─ Following ────────────────────────────────────╮\n" +
		"│ The case for slow blogging                     │\n" +
		"│ drewdevault.com · 14d ago · by drew            │\n" +
		"╰────────────────────────────────────────────────╯\n" +
		"╭─ News ─────────────────────────────────────────╮\n" +
		"│ Show HN: a tiny Go CLI for terminal news       │\n" +
		"│ github.com · 4h ago · by alice                 │\n" +
		"╰────────────────────────────────────────────────╯\n"
	if got != want {
		t.Errorf("two-pool render mismatch\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

// TestPools_EmptyPoolsDegrade walks the 3 → 2 → 1 collapse. The two-empty
// row is the load-bearing one: it must equal the unlabelled single-pool
// render, because that is the shape a user with one working pool sees.
func TestPools_EmptyPoolsDegrade(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	stories := poolFixture(now)
	opts := render.MultiOptions{Marker: render.TickerDot}
	full := []render.Pool{
		{Name: "following", Label: "Following", Stories: stories[0:1]},
		{Name: "news", Label: "News", Stories: stories[1:2]},
		{Name: "repos", Label: "Repos", Stories: stories[2:3]},
	}
	labelled := func(p render.Pool) string {
		o := opts
		o.Header = p.Label
		return mustMultiForPools(t, p.Stories, now, 50, o)
	}
	cases := []struct {
		name  string
		pools []render.Pool
		want  string
	}{
		{
			name:  "no empties keeps three labelled boxes",
			pools: full,
			want:  labelled(full[0]) + labelled(full[1]) + labelled(full[2]),
		},
		{
			name: "one empty leaves two labelled boxes",
			pools: []render.Pool{
				full[0],
				{Name: "news", Label: "News"},
				full[2],
			},
			want: labelled(full[0]) + labelled(full[2]),
		},
		{
			name: "two empty leaves one box with no header at all",
			pools: []render.Pool{
				{Name: "following", Label: "Following"},
				full[1],
				{Name: "repos", Label: "Repos", Stories: []fetch.Story{}},
			},
			want: mustMultiForPools(t, full[1].Stories, now, 50, opts),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mustPools(t, tc.pools, now, 50, opts)
			if got != tc.want {
				t.Errorf("degrade mismatch\n--- got ---\n%s--- want ---\n%s", got, tc.want)
			}
		})
	}
}

// TestPools_AllEmptyIsSilent: an empty render is a legitimate outcome, not
// an error. The caller chooses between fallback text and printing nothing,
// so Pools must not decide for it.
func TestPools_AllEmptyIsSilent(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		pools []render.Pool
	}{
		{"nil slice", nil},
		{"empty slice", []render.Pool{}},
		{
			name: "every pool empty",
			pools: []render.Pool{
				{Name: "following", Label: "Following"},
				{Name: "news", Label: "News", Stories: []fetch.Story{}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := render.Pools(tc.pools, now, 50, render.MultiOptions{Marker: render.TickerDot})
			if err != nil {
				t.Fatalf("Pools returned error %v, want nil", err)
			}
			if got != "" {
				t.Errorf("Pools = %q, want empty string", got)
			}
		})
	}
}

// jsonPoolFixture builds two pools with deterministic ages: the following
// pool carries one feed-attributed story, the news pool two aggregator
// stories. Kept local to the JSON/minimal tests so the box-render
// fixtures above stay free to change independently.
func jsonPoolFixture(now time.Time) []render.Pool {
	return []render.Pool{
		{
			Name:  "following",
			Label: "Following",
			Stories: []fetch.Story{{
				ID:        "f-1",
				Title:     "The case for slow blogging",
				URL:       "https://drewdevault.com/slow",
				Source:    "following",
				Feed:      "https://drewdevault.com/blog/index.xml",
				CreatedAt: now.Add(-48 * time.Hour),
				Tags:      []string{"writing"},
			}},
		},
		{
			Name:  "news",
			Label: "News",
			Stories: []fetch.Story{
				{
					ID:        "hn-1",
					Title:     "React 21 drops with native signals",
					URL:       "https://reactjs.org/",
					Source:    "hackernews",
					Points:    420,
					CreatedAt: now.Add(-2 * time.Hour),
					Tags:      []string{},
				},
				{
					ID:        "hn-2",
					Title:     "Rust 1.87 stabilizes async closures",
					URL:       "https://rust-lang.org/",
					Source:    "hackernews",
					Points:    300,
					CreatedAt: now.Add(-3 * time.Hour),
					Tags:      []string{},
				},
			},
		},
	}
}

// jsonPoolElement is the decode target for the wire shape. Declaring it
// here (rather than reusing render's unexported payload) is deliberate:
// the test asserts the SERIALISED contract, so it must not share a struct
// with the implementation.
type jsonPoolElement struct {
	Title      string   `json:"title"`
	URL        string   `json:"url"`
	Source     string   `json:"source"`
	AgeSeconds int64    `json:"age_seconds"`
	Tags       []string `json:"tags"`
	Pool       string   `json:"pool"`
}

func TestJSONPools_FlatArrayStampsEveryElement(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	out := render.JSONPools(jsonPoolFixture(now), now)

	if !strings.HasSuffix(out, "\n") {
		t.Errorf("JSONPools output must end with a newline for shell pipelines: %q", out)
	}
	var got []jsonPoolElement
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not a top-level JSON array: %v; output was %q", err, out)
	}
	if len(got) != 3 {
		t.Fatalf("got %d elements, want 3 (both pools flattened into one array): %q", len(got), out)
	}
	wantPools := []string{"following", "news", "news"}
	wantTitles := []string{
		"The case for slow blogging",
		"React 21 drops with native signals",
		"Rust 1.87 stabilizes async closures",
	}
	wantAges := []int64{172800, 7200, 10800}
	for i := range got {
		if got[i].Pool != wantPools[i] {
			t.Errorf("element %d: pool = %q, want %q", i, got[i].Pool, wantPools[i])
		}
		if got[i].Title != wantTitles[i] {
			t.Errorf("element %d: title = %q, want %q", i, got[i].Title, wantTitles[i])
		}
		if got[i].AgeSeconds != wantAges[i] {
			t.Errorf("element %d: age_seconds = %d, want %d", i, got[i].AgeSeconds, wantAges[i])
		}
	}
	if got[0].Source != "following" || got[1].Source != "hackernews" {
		t.Errorf("source field not carried through: %+v", got)
	}
	if got[0].URL != "https://drewdevault.com/slow" {
		t.Errorf("url field not carried through: %q", got[0].URL)
	}
	// age_seconds is int64 on the wire: no decimal point, ever.
	if strings.Contains(out, `"age_seconds":7200.`) {
		t.Errorf("age_seconds serialised as a float: %q", out)
	}
}

func TestJSONPools_NilTagsMarshalAsEmptyArray(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	pools := []render.Pool{{
		Name:  "news",
		Label: "News",
		Stories: []fetch.Story{{
			Title:     "x",
			URL:       "https://example.com/x",
			Source:    "hackernews",
			CreatedAt: now,
			Tags:      nil, // nil on input must still serialise as []
		}},
	}}
	out := render.JSONPools(pools, now)
	if strings.Contains(out, `"tags":null`) {
		t.Errorf("nil Tags must serialise as [], not null: %q", out)
	}
	if !strings.Contains(out, `"tags":[]`) {
		t.Errorf("expected tags:[] in %q", out)
	}
}

func TestJSONPools_NegativeAgeClampsToZero(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	pools := []render.Pool{{
		Name:  "news",
		Label: "News",
		Stories: []fetch.Story{{
			Title:  "from the future",
			URL:    "https://example.com/future",
			Source: "hackernews",
			// Clock skew between the source's timestamps and this host.
			CreatedAt: now.Add(90 * time.Second),
			Tags:      []string{},
		}},
	}}
	var got []jsonPoolElement
	if err := json.Unmarshal([]byte(render.JSONPools(pools, now)), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d elements, want 1", len(got))
	}
	if got[0].AgeSeconds != 0 {
		t.Errorf("age_seconds = %d for a future timestamp, want 0 (clamped like rank.Score)", got[0].AgeSeconds)
	}
}

func TestJSONPools_EmptyPoolsContributeNothing(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	pools := []render.Pool{
		{Name: "following", Label: "Following"}, // enabled but cold
		{Name: "news", Label: "News", Stories: []fetch.Story{{
			Title: "only story", URL: "https://example.com/a",
			Source: "hackernews", CreatedAt: now.Add(-time.Hour), Tags: []string{},
		}}},
	}
	var got []jsonPoolElement
	if err := json.Unmarshal([]byte(render.JSONPools(pools, now)), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 || got[0].Pool != "news" {
		t.Fatalf("empty pool leaked into the array: %+v", got)
	}

	// Every pool empty is still a valid, parseable array — never null.
	allEmpty := render.JSONPools([]render.Pool{{Name: "news", Label: "News"}}, now)
	if allEmpty != "[]\n" {
		t.Errorf("all-empty JSONPools = %q, want %q", allEmpty, "[]\n")
	}
}

// TestJSONPools_SingleStoryIsAOneElementArray_R3ContractBreak pins the
// deliberate break: --style=json used to emit a BARE OBJECT when exactly
// one story rendered. Ruling R-3 replaced that with a uniform array
// carrying a pool field on every element. If this test starts failing
// because someone restored the bare-object shape, the fix is to restore
// this expectation, not the old code.
func TestJSONPools_SingleStoryIsAOneElementArray_R3ContractBreak(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	pools := []render.Pool{{
		Name:  "news",
		Label: "News",
		Stories: []fetch.Story{{
			Title:     "A seeded story",
			URL:       "https://example.com/x",
			Source:    "hackernews",
			CreatedAt: now.Add(-2 * time.Hour),
			Tags:      []string{},
		}},
	}}
	out := render.JSONPools(pools, now)
	if !strings.HasPrefix(out, "[") {
		t.Fatalf("single-story output must be an array, not a bare object: %q", out)
	}
	var got []jsonPoolElement
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal: %v; output was %q", err, out)
	}
	if len(got) != 1 {
		t.Fatalf("got %d elements, want exactly 1: %q", len(got), out)
	}
	if got[0].Pool != "news" {
		t.Errorf("pool = %q, want %q — the stamp is unconditional now", got[0].Pool, "news")
	}
}

func TestMinimalPools_OneBlankLineBetweenPoolsOnly(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	pools := jsonPoolFixture(now)
	got := render.MinimalPools(pools, now)

	want := render.Minimal(pools[0].Stories[0], now) +
		"\n" +
		render.Minimal(pools[1].Stories[0], now) +
		render.Minimal(pools[1].Stories[1], now)
	if got != want {
		t.Errorf("MinimalPools mismatch\n got: %q\nwant: %q", got, want)
	}
	if strings.HasPrefix(got, "\n") {
		t.Error("MinimalPools emitted a leading blank line")
	}
	if strings.HasSuffix(got, "\n\n") {
		t.Error("MinimalPools emitted a trailing blank line")
	}
	// No pool labels: labels are box chrome (R-20).
	for _, label := range []string{"Following", "News"} {
		if strings.Contains(got, label) {
			t.Errorf("MinimalPools leaked the pool label %q: %q", label, got)
		}
	}
}

// TestMinimalPools_SinglePoolIsByteIdenticalToTodaysOutput is the
// non-regression guard for the overwhelmingly common configuration: one
// enabled pool must produce exactly the stacked render.Minimal lines the
// pre-pool dispatcher produced, with no separator anywhere.
func TestMinimalPools_SinglePoolIsByteIdenticalToTodaysOutput(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	news := jsonPoolFixture(now)[1]

	got := render.MinimalPools([]render.Pool{news}, now)
	want := render.Minimal(news.Stories[0], now) + render.Minimal(news.Stories[1], now)
	if got != want {
		t.Errorf("single-pool MinimalPools moved\n got: %q\nwant: %q", got, want)
	}
}

// TestMinimalPools_EmptyPoolAddsNoSeparator covers the degrade rule: an
// enabled-but-empty pool must not leave a blank line behind, or a cold
// following pool would prepend one to every minimal render.
func TestMinimalPools_EmptyPoolAddsNoSeparator(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	fixture := jsonPoolFixture(now)
	pools := []render.Pool{
		{Name: "following", Label: "Following"}, // cold
		fixture[1],
	}
	got := render.MinimalPools(pools, now)
	want := render.Minimal(fixture[1].Stories[0], now) + render.Minimal(fixture[1].Stories[1], now)
	if got != want {
		t.Errorf("empty pool changed the render\n got: %q\nwant: %q", got, want)
	}

	if all := render.MinimalPools([]render.Pool{{Name: "news", Label: "News"}}, now); all != "" {
		t.Errorf("all-empty MinimalPools = %q, want the empty string", all)
	}
}
