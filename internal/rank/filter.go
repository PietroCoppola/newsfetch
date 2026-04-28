package rank

import "github.com/PietroCoppola/newsfetch/internal/fetch"

// Filter returns the subset of stories whose Hash is not in seen. Order is
// preserved. The seen set is cheap to construct via history.File.HashSet,
// and pre-filtering before Score keeps the scoring function pure — the
// "already shown" rule never has to be tangled into the ranking math.
func Filter(stories []fetch.Story, seen map[string]struct{}) []fetch.Story {
	if len(seen) == 0 {
		return stories
	}
	out := stories[:0:0]
	for _, s := range stories {
		if _, hit := seen[s.Hash()]; hit {
			continue
		}
		out = append(out, s)
	}
	return out
}
