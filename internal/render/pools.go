package render

import (
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
