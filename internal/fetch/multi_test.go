package fetch_test

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/PietroCoppola/newsfetch/internal/fetch"
)

type stubSource struct {
	name    string
	stories []fetch.Story
	err     error
}

func (s *stubSource) Name() string { return s.name }
func (s *stubSource) Fetch(_ context.Context, _ fetch.FetchOptions) ([]fetch.Story, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.stories, nil
}

func TestFetchAll_AllSucceed(t *testing.T) {
	a := &stubSource{name: "alpha", stories: []fetch.Story{{ID: "a1"}, {ID: "a2"}}}
	b := &stubSource{name: "beta", stories: []fetch.Story{{ID: "b1"}}}

	stories, errs := fetch.FetchAll(context.Background(), []fetch.Source{a, b}, fetch.FetchOptions{})
	if errs != nil {
		t.Errorf("errs = %v, want nil (all succeeded)", errs)
	}
	ids := storyIDs(stories)
	want := []string{"a1", "a2", "b1"}
	if !equalStringSets(ids, want) {
		t.Errorf("merged ids = %v, want set %v", ids, want)
	}
}

func TestFetchAll_PartialFailure(t *testing.T) {
	good := &stubSource{name: "hackernews", stories: []fetch.Story{{ID: "hn-1"}}}
	bad := &stubSource{name: "lobsters", err: errors.New("503 Service Unavailable")}

	stories, errs := fetch.FetchAll(context.Background(), []fetch.Source{good, bad}, fetch.FetchOptions{})

	if len(stories) != 1 || stories[0].ID != "hn-1" {
		t.Errorf("expected one story from good source; got %v", stories)
	}
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want exactly one entry", errs)
	}
	if _, ok := errs["lobsters"]; !ok {
		t.Errorf("errs missing lobsters entry: %v", errs)
	}
	if _, ok := errs["hackernews"]; ok {
		t.Errorf("errs should not contain successful source: %v", errs)
	}
}

func TestFetchAll_AllFail(t *testing.T) {
	a := &stubSource{name: "alpha", err: errors.New("a is down")}
	b := &stubSource{name: "beta", err: errors.New("b is down")}

	stories, errs := fetch.FetchAll(context.Background(), []fetch.Source{a, b}, fetch.FetchOptions{})

	if len(stories) != 0 {
		t.Errorf("stories = %v, want empty", stories)
	}
	if len(errs) != 2 {
		t.Errorf("errs = %v, want 2 entries", errs)
	}
}

func TestFetchAll_LobstersFailureHNFlowsThrough(t *testing.T) {
	// Explicit Lobsters-503 case asked for in M4 plan: Lobsters has
	// historically been down for hours; the failure mode deserves dedicated
	// coverage.
	hn := &stubSource{
		name: "hackernews",
		stories: []fetch.Story{
			{ID: "hn-1", Title: "real news"},
			{ID: "hn-2", Title: "more real news"},
		},
	}
	lob := &stubSource{name: "lobsters", err: errors.New("lobsters request: status 503")}

	stories, errs := fetch.FetchAll(context.Background(), []fetch.Source{hn, lob}, fetch.FetchOptions{})

	if len(stories) != 2 {
		t.Errorf("expected 2 stories from HN; got %d (%v)", len(stories), stories)
	}
	if errs["lobsters"] == nil {
		t.Errorf("lobsters error should be present in errs map")
	}
	if errs["hackernews"] != nil {
		t.Errorf("hackernews entry should be absent from errs map")
	}
	// Convention check: len(errs) < len(sources) → partial failure, not total.
	if len(errs) >= 2 {
		t.Errorf("len(errs) = %d, want < 2 for partial-failure case", len(errs))
	}
}

func TestFetchAll_SingleSourceSuccess(t *testing.T) {
	s := &stubSource{name: "only", stories: []fetch.Story{{ID: "x"}}}
	stories, errs := fetch.FetchAll(context.Background(), []fetch.Source{s}, fetch.FetchOptions{})
	if errs != nil {
		t.Errorf("errs = %v, want nil", errs)
	}
	if len(stories) != 1 {
		t.Errorf("stories = %v, want one", stories)
	}
}

func TestFetchAll_EmptySources(t *testing.T) {
	stories, errs := fetch.FetchAll(context.Background(), nil, fetch.FetchOptions{})
	if stories != nil || errs != nil {
		t.Errorf("FetchAll(nil) = (%v, %v), want both nil", stories, errs)
	}
}

func storyIDs(stories []fetch.Story) []string {
	ids := make([]string, len(stories))
	for i, s := range stories {
		ids[i] = s.ID
	}
	return ids
}

func equalStringSets(a, b []string) bool {
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	if len(ac) != len(bc) {
		return false
	}
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}
