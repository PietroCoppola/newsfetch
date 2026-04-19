// Package rank scores cached stories and picks one via weighted stochastic
// selection. All exported functions are free of global state; Select takes
// an injected *rand.Rand so callers (main + tests) control determinism.
package rank

import (
	"math"
	"math/rand"
	"strings"
	"time"
	"unicode"

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

// Score returns the ranking score for s given the user's topics and the
// reference time now. Pure function; deterministic. Formula per spec §6:
//
//	score = (points / (age_hours + 2)^1.8) * topic_multiplier
//
// topic_multiplier is 2.0 when any configured topic matches the title,
// else 1.0. The spec's 1.5x tags-overlap branch is deferred to M4 (see
// design spec §2, resolved ambiguity #2).
func Score(s fetch.Story, topics []string, now time.Time) float64 {
	ageHours := now.Sub(s.CreatedAt).Hours()
	if ageHours < 0 {
		ageHours = 0
	}
	base := float64(s.Points) / math.Pow(ageHours+2, 1.8)
	if matchesAnyTopic(s.Title, topics) {
		return base * 2.0
	}
	return base
}

// matchesAnyTopic reports whether any topic matches the title.
//
// Single-word topic (no whitespace): split title on non-alphanumeric,
// lowercase, exact token match.
//
// Multi-word topic (contains whitespace, e.g. "machine learning"): case-
// insensitive substring match against the full title. M4 must not
// regress this into token match when adding the 1.5x tags branch —
// the tests in TestScore_TopicMatchTable guard this.
func matchesAnyTopic(title string, topics []string) bool {
	if len(topics) == 0 {
		return false
	}
	titleLower := strings.ToLower(title)
	var tokens []string // lazy — only build for single-word topics
	for _, t := range topics {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		tLower := strings.ToLower(t)
		if strings.ContainsAny(tLower, " \t") {
			if strings.Contains(titleLower, tLower) {
				return true
			}
			continue
		}
		if tokens == nil {
			tokens = tokenize(title)
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
