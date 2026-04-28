package rank_test

import (
	"reflect"
	"testing"

	"github.com/PietroCoppola/newsfetch/internal/fetch"
	"github.com/PietroCoppola/newsfetch/internal/rank"
)

func TestFilter(t *testing.T) {
	stories := []fetch.Story{
		{ID: "1", URL: "https://example.com/a", Source: "hackernews"},
		{ID: "2", URL: "https://example.com/b", Source: "hackernews"},
		{ID: "3", URL: "https://example.com/c", Source: "hackernews"},
	}

	cases := []struct {
		name     string
		seen     map[string]struct{}
		wantIDs  []string
		wantSame bool // returned slice should be the input slice unchanged
	}{
		{
			name:     "empty seen returns input",
			seen:     map[string]struct{}{},
			wantIDs:  []string{"1", "2", "3"},
			wantSame: true,
		},
		{
			name:    "nil seen returns input",
			seen:    nil,
			wantIDs: []string{"1", "2", "3"},
		},
		{
			name:    "removes a single match",
			seen:    map[string]struct{}{"https://example.com/b": {}},
			wantIDs: []string{"1", "3"},
		},
		{
			name:    "removes all matches",
			seen:    map[string]struct{}{"https://example.com/a": {}, "https://example.com/c": {}},
			wantIDs: []string{"2"},
		},
		{
			name:    "all seen returns empty",
			seen:    map[string]struct{}{"https://example.com/a": {}, "https://example.com/b": {}, "https://example.com/c": {}},
			wantIDs: []string{},
		},
		{
			name:    "unrelated hashes ignored",
			seen:    map[string]struct{}{"https://other.com/x": {}},
			wantIDs: []string{"1", "2", "3"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rank.Filter(stories, tc.seen)
			gotIDs := make([]string, len(got))
			for i, s := range got {
				gotIDs[i] = s.ID
			}
			if !reflect.DeepEqual(gotIDs, tc.wantIDs) {
				t.Errorf("IDs = %v, want %v", gotIDs, tc.wantIDs)
			}
		})
	}
}

func TestFilter_FallbackHashKeys(t *testing.T) {
	// Stories without a URL hash as source:id; Filter should match those
	// keys too.
	stories := []fetch.Story{
		{ID: "ask-1", URL: "", Source: "lobsters"},
		{ID: "1", URL: "https://example.com/a", Source: "hackernews"},
	}
	seen := map[string]struct{}{"lobsters:ask-1": {}}
	got := rank.Filter(stories, seen)
	if len(got) != 1 || got[0].ID != "1" {
		t.Errorf("got %v, want [{ID:1}]", got)
	}
}
