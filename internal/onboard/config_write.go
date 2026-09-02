package onboard

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrConfigExists is returned by WriteConfig when the target path already
// exists. The wizard surfaces this as a refusal so the user can delete or
// edit the file manually before re-running --init. OverwriteConfig (used by
// --settings) does not raise it; --settings is the explicit edit-existing
// path.
var ErrConfigExists = errors.New("config file already exists")

// WriteConfig writes a TOML config file capturing the wizard's answers.
// Parent directories are created as needed. If path already exists,
// WriteConfig returns ErrConfigExists without touching the file.
//
// topics and style are always emitted. pools and the [news] table are
// emitted iff the corresponding Answers field is non-nil — leaving them nil
// makes future default changes flow through to the user without requiring
// them to re-edit the file. Feed blocks are emitted whenever Answers.Feeds
// is non-empty, enabled pool or not. cache_ttl_minutes and min_points are
// never emitted (same reason).
func WriteConfig(path string, answers Answers) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%w: %s", ErrConfigExists, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat config: %w", err)
	}
	return writeConfigBytes(path, answers)
}

// OverwriteConfig writes (or replaces) a TOML config file. Used by
// --settings, which is the explicit edit-existing-config path; refusing on
// existing files would defeat its purpose. Same field-emission rules as
// WriteConfig.
func OverwriteConfig(path string, answers Answers) error {
	return writeConfigBytes(path, answers)
}

// writeConfigBytes is the shared write-and-mkdir core for WriteConfig and
// OverwriteConfig. Pulled out so the existence check stays the only
// difference between the two public entry points.
func writeConfigBytes(path string, answers Answers) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(renderConfigTOML(answers)), 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// renderConfigTOML produces the TOML body. Kept separate from WriteConfig
// for golden-style testing.
//
// ORDERING IS LOad-BEARING. This is line-oriented string building, not a
// TOML marshaller, so it has no notion of "which table am I in". Once a
// [table] header has been written, every subsequent key belongs to that
// table: a `count = 1` emitted after `[news]` becomes news.count, which the
// loader ignores, and the user's count silently reverts to the default. All
// top-level scalars are therefore written first, then [news], then the
// [[following.feeds]] array of tables. Do not move a scalar below a header.
//
// count, following_count, ticker_marker, and ticker_boxed are emitted
// unconditionally (even when currently inert because style != "boxed", the
// counts are 1, or the following pool is disabled) so a user's prior tuning
// survives a temporary switch away. This mirrors the wizard's
// hide-don't-clear behaviour for the same fields.
//
// pools, pool_order, and the [news] table follow the nil-means-omit
// convention: a nil slice leaves the key out of the file so a future change
// to the compile-time default reaches the user. pool_order additionally
// requires two or more enabled pools — a single-pool config has nothing to
// order and the wizard never asks.
//
// Feeds are always emitted when present, whether or not the following pool
// is enabled. Removing "following" from pools must not delete the user's
// subscriptions; re-enabling the pool restores them.
func renderConfigTOML(a Answers) string {
	var b strings.Builder
	b.WriteString("# newsfetch config. Edit freely; see spec.md for field meanings.\n\n")
	b.WriteString(renderStringArray("topics", a.Topics))
	fmt.Fprintf(&b, "style = %q\n", a.Style)
	if a.Pools != nil {
		b.WriteString(renderStringArray("pools", a.Pools))
	}
	if len(a.Pools) >= 2 && len(a.PoolOrder) > 0 {
		b.WriteString(renderStringArray("pool_order", a.PoolOrder))
	}
	fmt.Fprintf(&b, "count = %d\n", a.Count)
	fmt.Fprintf(&b, "following_count = %d\n", a.FollowingCount)
	fmt.Fprintf(&b, "ticker_marker = %q\n", a.TickerMarker)
	fmt.Fprintf(&b, "ticker_boxed = %t\n", a.TickerBoxed)
	// Everything below this line is inside a table. No top-level scalars.
	if a.NewsAggregators != nil {
		b.WriteString("\n[news]\n")
		b.WriteString(renderStringArray("aggregators", a.NewsAggregators))
	}
	for _, f := range a.Feeds {
		b.WriteString("\n[[following.feeds]]\n")
		fmt.Fprintf(&b, "url = \"%s\"\n", tomlEscape(f.URL))
		if f.MaxItems != nil {
			fmt.Fprintf(&b, "max_items = %d\n", *f.MaxItems)
		}
		if f.Weight != nil {
			// An empty literal means tomlFloat refused the value; see its
			// doc comment. Omitting the key is the safe outcome — the
			// loader reads a missing weight as "no manual override".
			if lit := tomlFloat(*f.Weight); lit != "" {
				fmt.Fprintf(&b, "weight = %s\n", lit)
			}
		}
	}
	return b.String()
}

// tomlFloat renders a float64 as a TOML float literal, or returns "" when
// the value has no honest literal. strconv would print a whole number as
// "5", which is a TOML *integer*; the file would then be relying on the
// decoder's willingness to coerce an int into a float64 field rather than
// saying what it means. Appending ".0" keeps the emitted type honest and
// keeps the output byte-stable across round trips.
//
// A NON-FINITE value is refused. FormatFloat renders NaN as "NaN" and an
// infinity as "+Inf", none of which contain ".", so the fractional-part
// rule above would append one and emit `weight = NaN.0` — not valid TOML,
// which means the next config.Load fails outright and the user's config
// file is corrupt. Validation should never let a non-finite weight reach
// this function (config.Validate clamps one, and validateFeeds rejects one
// at the JSON boundary), so this is defence in depth for the one path that
// skips both: --settings loads config.toml WITHOUT validating it, so a
// hand-written `weight = nan` travels straight from disk to here.
//
// Returning "" rather than an error is deliberate: renderConfigTOML builds
// a string and has no error path, and threading one through it for a case
// validation already covers would buy nothing. Omitting the key is also the
// better failure — a missing weight means "no manual override", which is
// exactly what a meaningless number should decay to.
func tomlFloat(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return ""
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// renderStringArray emits one TOML key = ["a", "b"] line, with [] for empty.
// Strings are escaped via tomlEscape since topics are user-supplied.
func renderStringArray(key string, vals []string) string {
	var b strings.Builder
	if len(vals) == 0 {
		fmt.Fprintf(&b, "%s = []\n", key)
		return b.String()
	}
	fmt.Fprintf(&b, "%s = [", key)
	for i, v := range vals {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(`"`)
		b.WriteString(tomlEscape(v))
		b.WriteString(`"`)
	}
	b.WriteString("]\n")
	return b.String()
}

// tomlEscape escapes the minimal set of characters that can appear in a
// user-supplied string (topic, aggregator name, feed URL).
func tomlEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
