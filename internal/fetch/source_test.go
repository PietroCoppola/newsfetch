package fetch_test

import (
	"testing"

	"github.com/PietroCoppola/newsfetch/internal/fetch"
)

// TestKnownSourceNames_ReturnsCopy pins the registry's immutability: a
// caller mutating the returned slice must not affect what later callers
// see (the validator and the wizards all consume this list).
func TestKnownSourceNames_ReturnsCopy(t *testing.T) {
	a := fetch.KnownSourceNames()
	a[0] = "mutated"
	if b := fetch.KnownSourceNames(); b[0] == "mutated" {
		t.Error("KnownSourceNames shares its backing array; want a fresh copy per call")
	}
}
