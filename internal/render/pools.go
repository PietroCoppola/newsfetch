package render

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/fetch"
)

// Pool is one render unit: a named bundle of already-selected stories.
// Pools never rank against each other, so the caller hands them over in the
// order it wants them stacked and [Pools] does no sorting of its own.
type Pool struct {
	// Name is the machine-readable pool id ("news", "following"). It is
	// carried for the JSON style and for error messages; the boxed render
	// never shows it.
	Name string
	// Label is the human-readable header ("News", "Following") written into
	// a box's top border. It is caller-supplied data so the render package
	// never has to know the pool registry.
	Label string
	// Stories is the pool's selection, already filtered and ranked.
	Stories []fetch.Story
}

// Pools stacks one [Multi] render per non-empty pool, in the order given.
//
// Two rules make the multi-pool feature invisible to a single-pool user.
// First, a pool with no stories renders nothing at all, so a three-box
// render degrades to two and then to one cleanly. Second, headers appear
// only when two or more pools actually RENDER — not when two or more are
// enabled — so the last surviving box is byte-for-byte the render that
// shipped before pools existed. The single-pool case delegates straight to
// Multi with an empty Header rather than reimplementing it behind a
// conditional, which is what keeps that identity true by construction.
//
// Boxes stack flush: no blank separator between them. When every pool is
// empty the result is the empty string and a nil error — an empty render is
// a legitimate outcome here, and the caller decides between fallback text
// and silence.
func Pools(pools []Pool, now time.Time, width int, opts MultiOptions) (string, error) {
	live := make([]Pool, 0, len(pools))
	for _, p := range pools {
		if len(p.Stories) > 0 {
			live = append(live, p)
		}
	}
	switch len(live) {
	case 0:
		return "", nil
	case 1:
		opts.Header = ""
		out, err := Multi(live[0].Stories, now, width, opts)
		if err != nil {
			return "", fmt.Errorf("render pool %q: %w", live[0].Name, err)
		}
		return out, nil
	}
	var b strings.Builder
	for _, p := range live {
		poolOpts := opts
		poolOpts.Header = p.Label
		out, err := Multi(p.Stories, now, width, poolOpts)
		if err != nil {
			return "", fmt.Errorf("render pool %q: %w", p.Name, err)
		}
		b.WriteString(out)
	}
	return b.String(), nil
}

// payload is the on-the-wire shape of one story in --style=json output.
// It lives here as the single definition on purpose: the renderer carried
// two byte-identical copies of it before pools existed (one per JSON
// entry point), and a third was about to arrive with JSONPools. One copy
// means the field set and its tags cannot drift apart between entry
// points — the tags ARE the documented contract.
//
// Pool carries no omitempty: the binding contract (R-3) is a pool field
// on EVERY object, unconditionally. omitempty would make that promise
// conditional on the name being non-empty — an empty name would drop the
// key entirely rather than emit "pool":"" — which is the same
// shape-varies-with-circumstances failure R-3 exists to eliminate, just
// relocated from story-count to name-emptiness.
type payload struct {
	Title      string   `json:"title"`
	URL        string   `json:"url"`
	Source     string   `json:"source"`
	AgeSeconds int64    `json:"age_seconds"`
	Tags       []string `json:"tags"`
	Pool       string   `json:"pool"`
}

// newPayload converts one story into its wire form, applying the two
// normalisations the JSON contract promises: nil tags marshal as [] (a
// script indexing .tags should never meet null), and a negative age —
// clock skew between an upstream's timestamps and this host — clamps to
// zero, matching how rank.Score treats the same skew.
func newPayload(s fetch.Story, pool string, now time.Time) payload {
	tags := s.Tags
	if tags == nil {
		tags = []string{}
	}
	ageSeconds := int64(now.Sub(s.CreatedAt).Seconds())
	if ageSeconds < 0 {
		ageSeconds = 0
	}
	return payload{
		Title:      s.Title,
		URL:        s.URL,
		Source:     s.Source,
		AgeSeconds: ageSeconds,
		Tags:       tags,
		Pool:       pool,
	}
}

// JSONPools renders every pool's stories as ONE flat top-level array in
// pool order, stamping each element with the pool it came from.
//
// The shape is uniform by ruling R-3: one story or twenty, one pool or
// two, the answer is always an array of objects carrying a pool field.
// The older shapes — a bare object at count 1, an unstamped array above
// it — were a contract that changed with the story count, which is
// exactly the thing a scripted consumer cannot handle. Empty pools
// contribute nothing, and an all-empty input marshals to [] rather than
// null so the output is always a parseable array.
//
// No error return: json.Marshal over a slice of scalar-and-string-slice
// structs does not fail in practice. The trailing newline is intentional
// for shell pipelines.
func JSONPools(pools []Pool, now time.Time) string {
	out := make([]payload, 0, len(pools))
	for _, p := range pools {
		for _, s := range p.Stories {
			out = append(out, newPayload(s, p.Name, now))
		}
	}
	b, _ := json.Marshal(out)
	return string(b) + "\n"
}

// MinimalPools renders each non-empty pool's stories as stacked [Minimal]
// lines, separated by exactly one blank line BETWEEN pools — never before
// the first, never after the last. A single pool is therefore
// byte-identical to what the pre-pool dispatcher printed, which is the
// point: minimal style exists for tight prompt decorations, and a stray
// blank line is a visible regression there.
//
// No pool labels (R-20): labels are box chrome. The blank line is the
// only boundary minimal style draws.
func MinimalPools(pools []Pool, now time.Time) string {
	var b strings.Builder
	first := true
	for _, p := range pools {
		if len(p.Stories) == 0 {
			continue
		}
		if !first {
			b.WriteString("\n")
		}
		first = false
		for _, s := range p.Stories {
			b.WriteString(Minimal(s, now))
		}
	}
	return b.String()
}
