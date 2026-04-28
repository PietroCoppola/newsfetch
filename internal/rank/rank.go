// Package rank scores cached stories and picks one via weighted stochastic
// selection. All exported functions are free of global state; Select takes
// an injected *rand.Rand so callers (main + tests) control determinism.
package rank

import (
	"math"
	"math/rand"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/PietroCoppola/newsfetch/internal/defaults"
	"github.com/PietroCoppola/newsfetch/internal/fetch"
)

// pickWeightedIndex returns an index i into weights, chosen with probability
// proportional to weights[i]. If every weight is <= 0 the caller almost
// certainly has a bug; we pick uniformly to avoid a panic. Weights with
// value <= 0 never win (treated as zero).
func pickWeightedIndex(weights []float64, rng *rand.Rand) int {
	total := 0.0
	for _, w := range weights {
		if w > 0 {
			total += w
		}
	}
	if total == 0 {
		return rng.Intn(len(weights))
	}
	r := rng.Float64() * total
	acc := 0.0
	for i, w := range weights {
		if w <= 0 {
			continue
		}
		acc += w
		if r < acc {
			return i
		}
	}
	return len(weights) - 1
}

// Tuning constants for the scoring formula. All three are starting values
// open to revision once real-data dogfooding informs the call; see
// spec.md §15. Named here rather than inlined in [Score] so the surface
// of "what's tunable" is discoverable from the package, and so the call
// site reads as design intent rather than a wall of magic numbers.
const (
	// gravityExponent controls how aggressively older stories decay. The
	// 1.8 starting value follows HN's classic ranking algorithm.
	gravityExponent = 1.8
	// ageOffsetHours prevents fresh stories from getting near-infinite
	// scores by keeping the denominator away from zero.
	ageOffsetHours = 2.0
	// topicMatchMultiplier is the boost applied when any configured topic
	// matches the story's matching surface (title or tags).
	topicMatchMultiplier = 2.0
)

// Score returns the ranking score for s given the user's topics and the
// reference time now. Pure function; deterministic. Formula per spec §6:
//
//	score = (points / (age_hours + ageOffsetHours)^gravityExponent) * topic_multiplier
//
// topic_multiplier is [topicMatchMultiplier] when any configured topic
// matches the story's matching surface (title plus tags), else 1.0. M4
// widened "matches the title" to "matches the title or any tag" so
// source-provided tags from Lobste.rs (and future tagged sources)
// contribute to topic relevance without per-source branches in the
// ranker. The boost therefore fires on signals invisible to the user
// (tags), which is intentional — the cleaner semantic is "topic matched
// any of the story's relevance signals", and HN stories carry empty
// Tags so their behaviour is unchanged.
func Score(s fetch.Story, topics []string, now time.Time) float64 {
	ageHours := now.Sub(s.CreatedAt).Hours()
	if ageHours < 0 {
		ageHours = 0
	}
	base := float64(s.Points) / math.Pow(ageHours+ageOffsetHours, gravityExponent)
	if matchesAnyTopic(s, topics) {
		return base * topicMatchMultiplier
	}
	return base
}

// matchesAnyTopic reports whether any topic matches the story's matching
// surface, where the surface is the title joined with the tags by a
// newline separator. The newline:
//
//   - is a non-word character so tokenize splits cleanly on it;
//   - cannot legitimately appear in a topic string the user typed, so a
//     multi-word topic substring search will never match across the
//     title|tag seam (preventing e.g. topic "machine learning" matching
//     a story with title ending in "machine" and a tag "learning").
//
// Single-word topic (no whitespace): tokenize the surface and require an
// exact token match. This is what makes topic "as" not match "wasm" —
// "wasm" is a single token, not "w" + "as" + "m".
//
// Multi-word topic (contains whitespace, e.g. "machine learning"): case-
// insensitive substring match against the lowercased surface. Multi-word
// tags are uncommon in practice but if one appears (e.g. Lobsters tag
// "open source") the tokenizer treats it as multiple terms, which gives
// the right answer for single-word topics matching either word.
func matchesAnyTopic(s fetch.Story, topics []string) bool {
	if len(topics) == 0 {
		return false
	}
	surface := s.Title
	if len(s.Tags) > 0 {
		surface = s.Title + "\n" + strings.Join(s.Tags, "\n")
	}
	surfaceLower := strings.ToLower(surface)
	var tokens []string // lazy — only built when a single-word topic appears
	for _, t := range topics {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		tLower := strings.ToLower(t)
		if strings.ContainsAny(tLower, " \t") {
			if strings.Contains(surfaceLower, tLower) {
				return true
			}
			continue
		}
		if tokens == nil {
			tokens = tokenize(surface)
		}
		for _, tok := range tokens {
			if tok == tLower {
				return true
			}
		}
	}
	return false
}

// tokenize splits s into lowercased tokens of runs of letters+digits.
func tokenize(s string) []string {
	var tokens []string
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		if b.Len() > 0 {
			tokens = append(tokens, b.String())
			b.Reset()
		}
	}
	if b.Len() > 0 {
		tokens = append(tokens, b.String())
	}
	return tokens
}

// Options carries per-invocation selection parameters. PoolSize <= 0 uses
// defaults.RankPoolSize.
type Options struct {
	Topics   []string
	Now      time.Time
	PoolSize int
}

// Select scores every story in stories, keeps the top PoolSize candidates,
// and picks one weighted by score using rng.
//
// Callers must pass a non-empty slice. On empty input Select PANICS — the
// same contract M1's selectStory used. Returning a zero Story silently
// would propagate bugs; panic gives an immediate, traceable failure.
func Select(stories []fetch.Story, opts Options, rng *rand.Rand) fetch.Story {
	if len(stories) == 0 {
		panic("rank.Select: stories must be non-empty")
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

	weights := make([]float64, len(all))
	for i, sc := range all {
		weights[i] = sc.w
	}
	return all[pickWeightedIndex(weights, rng)].s
}
