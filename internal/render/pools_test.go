package render_test

import (
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
