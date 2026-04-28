package rank

import (
	"math/rand"
	"sort"

	"github.com/PietroCoppola/newsfetch/internal/defaults"
	"github.com/PietroCoppola/newsfetch/internal/fetch"
)

// Diversity multipliers applied to candidate scores during multi-story
// selection. Composing two reasons to suppress should compound, so they are
// applied multiplicatively: a candidate that shares both a tag and the URL
// host with an already-picked story gets its score multiplied by
// diversityTagPenalty * diversityHostPenalty.
//
// Tag is the heavier penalty because shared topic is the more common
// repeat-feel in practice — "two stories about Rust" hits the "already saw
// this" feeling more than "two stories from the same publication" does.
// Both values are starting points open to tuning; see spec.md §15.
const (
	diversityTagPenalty  = 0.4
	diversityHostPenalty = 0.6
)

// SelectN returns up to n stories from the pool. The first slot is picked by
// the same weighted-stochastic rule as [Select] so multi-story renders share
// a hero with single-story behaviour. Slots 2..n are filled deterministically
// by repeatedly taking the highest-scoring remaining candidate, with each
// candidate's effective score multiplied by [diversityMultiplier] against
// the stories already picked.
//
// If the pool has fewer than n entries SelectN returns what it has rather
// than padding or erroring — the caller decides whether "fewer than
// requested" needs to be surfaced.
//
// On empty input SelectN PANICS, matching [Select]'s contract. n <= 0 also
// panics; "select zero stories" is a caller bug, not a runtime condition.
func SelectN(stories []fetch.Story, n int, opts Options, rng *rand.Rand) []fetch.Story {
	if len(stories) == 0 {
		panic("rank.SelectN: stories must be non-empty")
	}
	if n <= 0 {
		panic("rank.SelectN: n must be positive")
	}
	pool := opts.PoolSize
	if pool <= 0 {
		pool = defaults.RankPoolSize
	}

	type scored struct {
		s fetch.Story
		w float64
	}
	all := make([]scored, len(stories))
	for i, s := range stories {
		all[i] = scored{s: s, w: Score(s, opts.Topics, opts.Now)}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].w > all[j].w })
	if len(all) > pool {
		all = all[:pool]
	}

	picked := make([]fetch.Story, 0, n)

	weights := make([]float64, len(all))
	for i, sc := range all {
		weights[i] = sc.w
	}
	heroIdx := pickWeightedIndex(weights, rng)
	picked = append(picked, all[heroIdx].s)
	all = append(all[:heroIdx], all[heroIdx+1:]...)

	for len(picked) < n && len(all) > 0 {
		bestIdx := 0
		bestEff := all[0].w * diversityMultiplier(all[0].s, picked)
		for i := 1; i < len(all); i++ {
			eff := all[i].w * diversityMultiplier(all[i].s, picked)
			if eff > bestEff {
				bestEff = eff
				bestIdx = i
			}
		}
		picked = append(picked, all[bestIdx].s)
		all = append(all[:bestIdx], all[bestIdx+1:]...)
	}

	return picked
}

// diversityMultiplier returns the score-attenuation factor for candidate
// given the already-picked stories. The factor is in (0, 1]; a candidate
// that shares no tag or host with anything picked returns 1.0 (no penalty).
func diversityMultiplier(candidate fetch.Story, picked []fetch.Story) float64 {
	m := 1.0
	candHost := candidate.NormalisedHost()
	for _, p := range picked {
		if candHost != "" && candHost == p.NormalisedHost() {
			m *= diversityHostPenalty
		}
		if sharesAnyTag(candidate.Tags, p.Tags) {
			m *= diversityTagPenalty
		}
	}
	return m
}

func sharesAnyTag(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	set := make(map[string]struct{}, len(a))
	for _, t := range a {
		set[t] = struct{}{}
	}
	for _, t := range b {
		if _, hit := set[t]; hit {
			return true
		}
	}
	return false
}
