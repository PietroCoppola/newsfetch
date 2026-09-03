package onboard

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"

	"github.com/PietroCoppola/newsfetch/internal/defaults"
	"github.com/PietroCoppola/newsfetch/internal/fetch"
)

// topicOptions defines the topic multi-select choices used by both wizards
// (one shared function keeps --init and --settings menus identical; adding
// or removing a topic is a one-line change). All option tables are
// functions, not package-level slices: no package-level mutable state is
// left exposed (the two wizards no longer share option structs), and
// package init does no avoidable work — though the dominant init cost on
// the render path is the huh/bubbletea stack itself, which links into the
// one binary regardless; see spec §12's honesty note.
func topicOptions() []huh.Option[string] {
	return []huh.Option[string]{
		huh.NewOption("AI / LLMs", "ai"),
		huh.NewOption("Rust", "rust"),
		huh.NewOption("Go", "go"),
		huh.NewOption("Python", "python"),
		huh.NewOption("JavaScript / TypeScript", "javascript"),
		huh.NewOption("Databases", "databases"),
		huh.NewOption("Security", "security"),
		huh.NewOption("Systems / OS / kernels", "systems"),
		huh.NewOption("DevOps / infrastructure", "devops"),
		huh.NewOption("Hardware", "hardware"),
	}
}

// styleOptions defines the display-style picker choices, used by both
// wizards. It deliberately does NOT offer "statusline": that style is
// valid from the --style flag only, and this list is one of three guards
// keeping it out of persisted config (the others are config.Validate and
// validateStyle). Persisted, it would make every terminal open render a
// bare linked line — or nothing at all on a cold cache, since the
// statusline path never blocks on the network.
func styleOptions() []huh.Option[string] {
	return []huh.Option[string]{
		huh.NewOption("Boxed (framed, default)", "boxed"),
		huh.NewOption("Minimal (one line)", "minimal"),
		huh.NewOption("JSON (machine-readable)", "json"),
	}
}

// countOptions defines the per-render story-count picker, surfaced in the
// settings wizard. Capped at defaults.MaxCount; values above turn hero+ticker
// into a list, which the spec deliberately rejects. Labels are kept tight
// for inline (single-row) display.
func countOptions() []huh.Option[int] {
	return []huh.Option[int]{
		huh.NewOption("1", 1),
		huh.NewOption("2", 2),
		huh.NewOption("3", 3),
		huh.NewOption("4", 4),
	}
}

// tickerMarkerOptions defines the ticker-marker picker. Names mirror
// render.KnownTickerMarkers; the labels carry a one-glyph preview so the
// user can tell them apart without remembering what each name draws.
func tickerMarkerOptions() []huh.Option[string] {
	return []huh.Option[string]{
		huh.NewOption("Dot · (default, neutral)", "dot"),
		huh.NewOption("Arrow ↳ (continuation)", "arrow"),
		huh.NewOption("Branch ├─ (tree)", "branch"),
	}
}

// tickerBoxedOptions defines the box-style picker for multi-story renders.
func tickerBoxedOptions() []huh.Option[bool] {
	return []huh.Option[bool]{
		huh.NewOption("Plain (hero box, ticker lines beneath)", false),
		huh.NewOption("Connected (one outer box around hero plus ticker)", true),
	}
}

// sourceOptions builds the source multi-select choices from the canonical
// list in fetch.KnownSourceNames so a new source automatically shows up
// in the --settings wizard without a second edit.
func sourceOptions() []huh.Option[string] {
	names := fetch.KnownSourceNames()
	opts := make([]huh.Option[string], 0, len(names))
	for _, name := range names {
		label := name
		switch name {
		case "hackernews":
			label = "Hacker News"
		case "lobsters":
			label = "Lobste.rs"
		}
		opts = append(opts, huh.NewOption(label, name))
	}
	return opts
}

// poolOptions defines the pool enable multi-select. The choices come from
// defaults.KnownPools so a pool cannot appear in the wizard before the
// renderer knows how to draw it — repos is designed but unimplemented in
// M5, and it is absent from the registry rather than filtered out here, so
// there is one place to change when it ships. Labels come from
// defaults.PoolLabel, the same function the box headers use, so the wizard
// and the render can never disagree about what a pool is called.
func poolOptions() []huh.Option[string] {
	names := defaults.KnownPools()
	opts := make([]huh.Option[string], 0, len(names))
	for _, n := range names {
		opts = append(opts, huh.NewOption(defaults.PoolLabel(n), n))
	}
	return opts
}

// poolFirstOptions defines the "which pool appears first?" picker, built
// from the pools the user actually enabled. One question is enough for two
// pools: naming the first settles the second. A third pool would need a
// cascading second question, which is why this takes the enabled list
// rather than assuming a pair.
func poolFirstOptions(pools []string) []huh.Option[string] {
	opts := make([]huh.Option[string], 0, len(pools))
	for _, p := range pools {
		opts = append(opts, huh.NewOption(defaults.PoolLabel(p), p))
	}
	return opts
}

// feedLabelWidth caps a feed URL's display length in the remove-feeds list.
// Sized to leave room for huh's checkbox gutter inside an 80-column
// terminal; a wrapped option turns the list into an unreadable wall.
const feedLabelWidth = 60

// feedRemoveOptions builds the remove-feeds multi-select from the feeds the
// user currently has. The label is truncated for display but the option
// VALUE is always the full URL, because removal matches on it exactly — a
// truncated value would quietly fail to remove the feed the user picked.
func feedRemoveOptions(feeds []Feed) []huh.Option[string] {
	opts := make([]huh.Option[string], 0, len(feeds))
	for _, f := range feeds {
		opts = append(opts, huh.NewOption(truncateURL(f.URL, feedLabelWidth), f.URL))
	}
	return opts
}

// truncateURL shortens raw to at most max characters, marking the cut with
// an ellipsis. It counts runes rather than bytes so a non-ASCII URL is not
// sliced mid-codepoint into a broken glyph.
func truncateURL(raw string, max int) string {
	r := []rune(raw)
	if len(r) <= max {
		return raw
	}
	if max <= 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}

// removeFeeds returns feeds with every entry whose URL appears in urls
// dropped. Order and the MaxItems/Weight pointers of the survivors are
// preserved exactly: the wizard does not surface those knobs, so it must
// not rewrite them, and removing one feed must never disturb another.
// Returns nil rather than an empty slice so the config writer's
// nil-means-omit convention sees what it expects.
func removeFeeds(feeds []Feed, urls []string) []Feed {
	if len(urls) == 0 {
		return feeds
	}
	drop := make(map[string]struct{}, len(urls))
	for _, u := range urls {
		drop[u] = struct{}{}
	}
	kept := make([]Feed, 0, len(feeds))
	for _, f := range feeds {
		if _, ok := drop[f.URL]; ok {
			continue
		}
		kept = append(kept, f)
	}
	if len(kept) == 0 {
		return nil
	}
	if len(kept) == len(feeds) {
		return feeds
	}
	return kept
}

// appendFeed adds url to feeds unless it is already present, carrying no
// advanced knobs — the wizard cannot invent a max_items or weight the user
// was never asked about. Whitespace is trimmed first because the add-feed
// loop reads a raw terminal line: a trailing space would otherwise create a
// second entry for the same feed, which then appears twice in the remove
// list and fetches twice on every refresh.
func appendFeed(feeds []Feed, url string) []Feed {
	url = strings.TrimSpace(url)
	if url == "" {
		return feeds
	}
	for _, f := range feeds {
		if f.URL == url {
			return feeds
		}
	}
	return append(feeds, Feed{URL: url})
}

// orderWithFirst produces a pool_order for the enabled pools with first at
// the front. The remaining pools follow defaults.PoolOrder()'s compile-time
// sequence rather than the order the user happened to check them in, so the
// single question the wizard asks — which pool appears first — is the only
// thing that decides the stacking. A first that is not enabled (the user
// unchecked the pool after picking it) is ignored, which degrades to the
// compile-time order rather than to a pool_order naming a disabled pool.
func orderWithFirst(first string, pools []string) []string {
	enabled := make(map[string]struct{}, len(pools))
	for _, p := range pools {
		enabled[p] = struct{}{}
	}
	if _, ok := enabled[first]; !ok {
		first = ""
	}
	order := make([]string, 0, len(pools))
	if first != "" {
		order = append(order, first)
	}
	for _, p := range defaults.PoolOrder() {
		if _, ok := enabled[p]; ok && p != first {
			order = append(order, p)
		}
	}
	// Anything the compile-time order does not know about goes last, so a
	// pool added to the registry without being added to PoolOrder is
	// misplaced rather than silently dropped.
	for _, p := range pools {
		if p == first || containsString(order, p) {
			continue
		}
		order = append(order, p)
	}
	if len(order) == 0 {
		return nil
	}
	return order
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// tickerGroupHidden reports whether the ticker-tuning group is inert. The
// knobs are box chrome for stacked stories, so they matter only under a
// boxed style, and — with two pools in play — as soon as EITHER pool's
// count crosses one. Checking Count alone would hide the marker picker from
// a user whose single news story sits above three following stories.
func tickerGroupHidden(a Answers) bool {
	n := a.Count
	if a.FollowingCount > n {
		n = a.FollowingCount
	}
	return a.Style != "boxed" || n <= 1
}

// requirePoolContent reports the error an enabled pool must show when it
// has nothing in it: no aggregators for news, no feeds for following. n is
// how many entries that pool currently holds.
//
// Returns nil when the pool is not in pools, and that is the whole point
// (ruling R-39). A user who turns a pool off is not asked to populate it,
// and a user who leaves one on does not get to finish with it empty. The
// two pools share this helper so they cannot drift into different rules;
// the form calls it from the aggregator field's validator and from the
// add-feed loop's empty submission.
//
// This is the wizard's half of a rule the other two surfaces already
// enforce their own way: config.Validate clamps an all-empty config back to
// the defaults, and the JSON readers reject it outright. The wizard is the
// one surface that can prevent the state from being created at all, so it
// does.
func requirePoolContent(pools []string, pool string, n int) error {
	if !containsString(pools, pool) || n > 0 {
		return nil
	}
	switch pool {
	case "news":
		return errors.New("pick at least one aggregator, or go back and turn the News pool off")
	case "following":
		return errors.New("add at least one feed, or turn the Following pool off")
	default:
		return fmt.Errorf("the %s pool is enabled but empty", pool)
	}
}

// Feed is one [[following.feeds]] entry as the wizard and the JSON readers
// see it.
//
// MaxItems and Weight are pointers on purpose. In the config file an absent
// key means "unset" — the loader substitutes defaults.DefaultFeedMaxItems
// and "no manual weight override" — and a plain int or float64 cannot tell
// an absent key from a key the user set to zero. The wizard never surfaces
// either knob (they are TOML/JSON-only advanced settings), so nil is the
// value a --settings round trip has to carry through untouched. Anything
// that loses the nil rewrites the user's file with a value they never chose.
type Feed struct {
	URL      string
	MaxItems *int     // nil → key omitted; defaults.DefaultFeedMaxItems applies
	Weight   *float64 // nil → key omitted; the auto cadence weight applies
}

// Answers captures wizard / JSON-stdin output for both --init and --settings.
//
// NewsAggregators is nil-vs-non-nil sensitive: nil means "the caller did not
// specify aggregators" (the config writer omits the whole [news] table so
// future default changes flow through), non-nil means "use exactly these"
// (the writer emits the table). Pools follows the same convention.
//
// Feeds carries the [[following.feeds]] blocks verbatim, including the
// advanced knobs the wizard does not surface. OverwriteConfig regenerates
// the entire config file from this struct, so a feed that does not survive
// the trip from config.Load to here is a feed erased from the user's disk.
//
// Count, FollowingCount, TickerMarker, and TickerBoxed are persisted
// unconditionally even when currently inert (e.g. TickerMarker survives a
// switch from style=boxed to style=minimal, FollowingCount survives
// disabling the following pool). The choice is deliberate: a user who
// previously tuned a render expects to find that tuning preserved when they
// switch back, rather than having to re-pick from defaults. The settings
// wizard mirrors this by hiding inert fields rather than clearing them.
type Answers struct {
	Topics          []string
	Style           string
	Pools           []string // nil → omit from config; non-nil → emit verbatim
	PoolOrder       []string // emitted only when two or more pools are enabled
	NewsAggregators []string // nil → omit the [news] table; non-nil → emit verbatim
	Count           int
	FollowingCount  int
	Feeds           []Feed
	TickerMarker    string
	TickerBoxed     bool
}

// RunInitWizard drives the interactive --init UI: a topic multi-select
// followed by a display-style picker. Pools, feeds, aggregators and counts
// are intentionally not surfaced — --init is the opinionated onboarding
// contract; users reach the rest via --settings or the JSON-stdin
// power-user path. Returns the user's choices over defaultInitAnswers'
// seeded defaults. Not unit-tested — the TUI is exercised via manual smoke,
// though initFields is pinned by a test.
func RunInitWizard() (Answers, error) {
	a := defaultInitAnswers()
	form := huh.NewForm(
		huh.NewGroup(initFields(&a)...),
	).WithKeyMap(initKeyMap())
	if err := form.Run(); err != nil {
		return Answers{}, err
	}
	return a, nil
}

// initFields is the --init wizard's field list, lifted out of RunInitWizard
// so a test can pin it. The set is a contract, not an implementation
// detail: --init asks two questions and no more, and a wizard that grows a
// field at a time is how a 15-second onboarding becomes a 90-second one.
// Adding a field here should have to delete a test that says why it exists.
func initFields(a *Answers) []huh.Field {
	return []huh.Field{
		huh.NewMultiSelect[string]().
			Title("Pick topics that interest you").
			Description("These bias which stories surface. Leave empty to see whatever's hot.").
			Filterable(false).
			Options(topicOptions()...).
			Value(&a.Topics),
		huh.NewSelect[string]().
			Title("Display style").
			Filtering(false).
			Options(styleOptions()...).
			Value(&a.Style),
	}
}

// defaultInitAnswers is the starting state for the --init wizard: every
// field the form does not surface is seeded with its compile-time default
// so the written config validates cleanly. renderConfigTOML persists
// count/following_count/ticker_marker/ticker_boxed unconditionally on the
// assumption that producers supply valid values — this seeding is what
// upholds that assumption for the interactive path, mirroring
// ReadInitJSON's seeding for the non-TTY path. Style is seeded too so the
// picker opens on the default rather than an empty selection.
//
// Pools is seeded to defaults.Pools() — news only. Following starts
// DISABLED with Feeds nil: --init asks two questions and a first-run user
// has no feeds to put in the pool, so enabling it would only produce an
// empty box. NewsAggregators stays nil so the writer omits the [news] table
// entirely and a future change to the default aggregator list reaches the
// user without them re-running --settings.
func defaultInitAnswers() Answers {
	return Answers{
		Style:          defaults.Style,
		Pools:          defaults.Pools(),
		Count:          defaults.Count,
		FollowingCount: defaults.FollowingCount,
		TickerMarker:   defaults.TickerMarker,
		TickerBoxed:    defaults.TickerBoxed,
	}
}

// RunSettingsWizard drives the interactive --settings UI, prefilled from
// current. It runs as three stages because huh imposes two limits: a hide
// predicate belongs to a Group, never to a single field, so every
// conditionally-visible question needs its own group; and there is no
// repeat-group primitive, so collecting an unknown number of feed URLs has
// to be a loop that runs a one-field form until the input comes back empty.
//
//	stage 1 — topics, pools, news aggregators, remove-feeds
//	stage 2 — the add-feed loop (skipped when following is disabled)
//	stage 3 — style, counts, pool order, ticker tuning
//
// A pool's contents are asked for, and required, only while that pool is
// enabled — see requirePoolContent. That is why the aggregator picker has a
// group to itself: huh hides groups, not fields.
//
// Content configuration comes first and presentation last, matching the
// arrangement --settings has always had.
//
// Hidden fields are preserved, never cleared: a user who disables the
// following pool keeps their feeds and their following_count, and finds
// both intact when they re-enable it. That is not just courtesy —
// OverwriteConfig regenerates the whole config file from what this function
// returns, so a value dropped here is a value deleted from the user's disk.
//
// Returns the user's edited choices. Not unit-tested — manual smoke; the
// pure helpers it calls are what carry the test coverage.
func RunSettingsWizard(current Answers) (Answers, error) {
	a := Answers{
		Topics:          append([]string(nil), current.Topics...),
		Style:           current.Style,
		Pools:           append([]string(nil), current.Pools...),
		PoolOrder:       append([]string(nil), current.PoolOrder...),
		NewsAggregators: append([]string(nil), current.NewsAggregators...),
		Count:           current.Count,
		FollowingCount:  current.FollowingCount,
		Feeds:           append([]Feed(nil), current.Feeds...),
		TickerMarker:    current.TickerMarker,
		TickerBoxed:     current.TickerBoxed,
	}
	if len(a.Pools) == 0 {
		a.Pools = defaults.Pools()
	}
	if a.FollowingCount == 0 {
		a.FollowingCount = defaults.FollowingCount
	}

	var removed []string
	content := huh.NewForm(
		// Group 1: always shown. What to read, and which pools supply it.
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Topics").
				Description("These bias which stories surface. Leave empty to see whatever's hot.").
				Filterable(false).
				Options(topicOptions()...).
				Value(&a.Topics),
			huh.NewMultiSelect[string]().
				Title("Pools").
				Description("Each enabled pool renders its own box. At least one required.").
				Filterable(false).
				Options(poolOptions()...).
				Validate(func(v []string) error {
					if len(v) == 0 {
						return errors.New("pick at least one pool")
					}
					return nil
				}).
				Value(&a.Pools),
		),
		// Group 2: where the News pool fetches from. Its own group so it can
		// be hidden — asking a user who just unchecked News to populate it
		// is a question about a pool they turned off, and the old shape
		// REQUIRED an answer, so an unchecked News pool could not be saved
		// at all. Required only while News is enabled (R-39).
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("News aggregators").
				Description("Where the News pool fetches from. At least one required.").
				Filterable(false).
				Options(sourceOptions()...).
				Validate(func(v []string) error {
					return requirePoolContent(a.Pools, "news", len(v))
				}).
				Value(&a.NewsAggregators),
		).WithHideFunc(func() bool {
			return !containsString(a.Pools, "news")
		}),
		// Group 3: removing existing feeds. Hidden when the following pool
		// is off or there is nothing to remove — an empty multi-select is a
		// dead end the user has to tab past.
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Remove feeds").
				Description("Check the feeds to unsubscribe from. Leave empty to keep them all.").
				Filterable(false).
				Options(feedRemoveOptions(current.Feeds)...).
				Value(&removed),
		).WithHideFunc(func() bool {
			return len(current.Feeds) == 0 || !containsString(a.Pools, "following")
		}),
	).WithKeyMap(settingsKeyMap())
	if err := content.Run(); err != nil {
		return Answers{}, err
	}
	a.Feeds = removeFeeds(a.Feeds, removed)

	if containsString(a.Pools, "following") {
		feeds, err := runAddFeedLoop(a.Pools, a.Feeds)
		if err != nil {
			return Answers{}, err
		}
		a.Feeds = feeds
	}

	first := defaults.PoolOrder()[0]
	if len(a.PoolOrder) > 0 {
		first = a.PoolOrder[0]
	}
	if !containsString(a.Pools, first) {
		first = a.Pools[0]
	}
	presentation := huh.NewForm(
		// Group 4: always shown. count is the News pool's knob and keeps
		// that meaning; --count sets the same field.
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Display style").
				Filtering(false).
				Options(styleOptions()...).
				Value(&a.Style),
			huh.NewSelect[int]().
				Title("News stories per render").
				Description("How many News stories appear each invocation.").
				Filtering(false).
				Inline(true).
				Options(countOptions()...).
				Value(&a.Count),
		),
		// Group 5: the following pool's own count. Its own group because
		// huh hides groups, not fields.
		huh.NewGroup(
			huh.NewSelect[int]().
				Title("Following stories per render").
				Description("How many stories from your feeds appear each invocation.").
				Filtering(false).
				Inline(true).
				Options(countOptions()...).
				Value(&a.FollowingCount),
		).WithHideFunc(func() bool {
			return !containsString(a.Pools, "following")
		}),
		// Group 6: stacking order. One question settles two pools; with a
		// single pool there is nothing to order.
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Which pool appears first?").
				Description("Boxes stack top to bottom in this order.").
				Filtering(false).
				Options(poolFirstOptions(a.Pools)...).
				Value(&first),
		).WithHideFunc(func() bool {
			return len(a.Pools) < 2
		}),
		// Group 7: only relevant when a boxed render stacks more than one
		// story in some pool. Hidden values are preserved, not cleared.
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Ticker marker").
				Description("Symbol prefixing each non-hero story.").
				Filtering(false).
				Options(tickerMarkerOptions()...).
				Value(&a.TickerMarker),
			huh.NewSelect[bool]().
				Title("Ticker box style").
				Filtering(false).
				Options(tickerBoxedOptions()...).
				Value(&a.TickerBoxed),
		).WithHideFunc(func() bool {
			return tickerGroupHidden(a)
		}),
	).WithKeyMap(settingsKeyMap())
	if err := presentation.Run(); err != nil {
		return Answers{}, err
	}

	// Only rewrite the order when there is an order to write. With one pool
	// the user's previous ordering is left alone in a.PoolOrder, the same
	// persist-don't-clear rule the feeds follow — but that only survives a
	// disable/re-enable within THIS run. The writer omits pool_order from
	// disk whenever fewer than two pools are enabled (config_write.go), so
	// a specific order set before disabling down to one pool is not there
	// to reload in a later, separate --settings invocation; re-enabling the
	// second pool then falls back to the compile-time default order.
	if len(a.Pools) >= 2 {
		a.PoolOrder = orderWithFirst(first, a.Pools)
	}
	return a, nil
}

// runAddFeedLoop collects new feed URLs one at a time. huh has no
// repeat-group primitive, so this constructs and runs a fresh one-field
// form each pass and stops on an empty submission.
//
// Validation is URL SYNTAX ONLY, via ValidateFeedURL. There is deliberately
// no reachability check: the wizard runs on whatever network the user
// happens to have, and blocking on a slow or offline host to reject a
// perfectly valid URL is the worse failure. An unreachable feed surfaces at
// refresh time instead, where retrying costs nothing.
//
// The empty submission — the way OUT of the loop — is itself validated
// through requirePoolContent, which is the Following pool's half of the
// enabled-pools-must-have-content rule (R-39). This loop only runs when
// Following is enabled, so finishing it with nothing subscribed would write
// exactly the enabled-but-empty pool that config.Validate clamps and the
// JSON readers reject. The description flips accordingly, so the first pass
// says a feed is needed and every later pass says how to stop.
func runAddFeedLoop(pools []string, feeds []Feed) ([]Feed, error) {
	for {
		var raw string
		desc := "RSS or Atom URL. Leave empty to finish."
		if len(feeds) == 0 {
			desc = "RSS or Atom URL. The Following pool is on, so at least one is required."
		}
		form := huh.NewForm(
			huh.NewGroup(
				huh.NewInput().
					Title("Add a feed").
					Description(desc).
					Placeholder("https://example.com/feed.xml").
					Validate(func(s string) error {
						if strings.TrimSpace(s) == "" {
							return requirePoolContent(pools, "following", len(feeds))
						}
						return ValidateFeedURL(s)
					}).
					Value(&raw),
			),
		).WithKeyMap(settingsKeyMap())
		if err := form.Run(); err != nil {
			return nil, err
		}
		if strings.TrimSpace(raw) == "" {
			return feeds, nil
		}
		feeds = appendFeed(feeds, raw)
	}
}

// initKeyMap is tuned for the 2-field --init wizard so tab cycles between
// topics and style:
//
//   - Toggle help shows "space/x" (both work; default surfaced only x).
//   - Field 1 (topics multi-select): tab forward, no back (Prev disabled).
//   - Field 2 (style select): tab back, enter submit. Mashing tab pings
//     between the two fields without a shift modifier.
//   - SelectAll bound to "a" (default ctrl+a feels overkill).
func initKeyMap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()

	km.MultiSelect.Toggle = key.NewBinding(key.WithKeys(" ", "x"), key.WithHelp("space/x", "toggle"))
	km.MultiSelect.Prev = key.NewBinding(key.WithDisabled())
	km.MultiSelect.Next = key.NewBinding(key.WithKeys("enter", "tab"), key.WithHelp("tab/enter", "next"))
	km.MultiSelect.SelectAll = key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "select all"))

	km.Select.Prev = key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "back"))
	km.Select.Next = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "submit"))

	return km
}

// settingsKeyMap is tuned for the --settings wizard's fields. Standard
// tab/shift+tab navigation: huh doesn't expose a clean way to make tab
// on the last field cycle back to the first (the public KeyMap is
// form-level, not per-field, and Next/Submit on the last field can't
// be redirected without forking huh).
//
// The bindings below exist primarily to make the footer help text
// consistent across every field. By default huh shows different labels per
// field type ("enter confirm" on a multi-select, "enter select" on a
// select) and leaves the filter binding visible even when
// Filtering/Filterable is off. We override them so a user reading the
// footer sees the same vocabulary regardless of which field has focus.
func settingsKeyMap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()

	km.MultiSelect.Toggle = key.NewBinding(key.WithKeys(" ", "x"), key.WithHelp("space/x", "toggle"))
	km.MultiSelect.SelectAll = key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "select all"))
	km.MultiSelect.Prev = key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "back"))
	km.MultiSelect.Next = key.NewBinding(key.WithKeys("tab", "enter"), key.WithHelp("tab/enter", "next"))
	// Submit is enter-only on purpose. Binding tab here would be a footgun:
	// a user expecting tab to cycle (impossible with huh) would accidentally
	// submit the form on the last field. Tab on the last field does nothing
	// (Next is invalid past the last field), which is safer than surprise
	// submit; enter is the explicit submit gesture, surfaced in the footer.
	km.MultiSelect.Submit = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "submit"))
	km.MultiSelect.Filter = key.NewBinding(key.WithDisabled())

	km.Select.Prev = key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "back"))
	km.Select.Next = key.NewBinding(key.WithKeys("tab", "enter"), key.WithHelp("tab/enter", "next"))
	km.Select.Submit = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "submit"))
	km.Select.Filter = key.NewBinding(key.WithDisabled())

	return km
}
