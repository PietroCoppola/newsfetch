package fetch_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/fetch"
)

func TestHackerNews_Name(t *testing.T) {
	h := &fetch.HackerNews{}
	if got, want := h.Name(), "hackernews"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
}

func TestHackerNews_Fetch_Success(t *testing.T) {
	var gotURL *url.URL
	body := `{
		"hits": [
			{
				"objectID": "42",
				"title": "A story",
				"url": "https://example.com/a",
				"points": 150,
				"author": "alice",
				"created_at": "2026-04-18T08:00:00Z"
			},
			{
				"objectID": "43",
				"title": "Another story",
				"url": "https://example.com/b",
				"points": 90,
				"author": "bob",
				"created_at": "2026-04-18T07:30:00Z"
			}
		]
	}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	defer ts.Close()

	h := &fetch.HackerNews{BaseURL: ts.URL, Client: ts.Client()}
	stories, err := h.Fetch(context.Background(), fetch.FetchOptions{MinPoints: 50, Limit: 30})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	want := []fetch.Story{
		{
			ID:        "hn-42",
			Title:     "A story",
			URL:       "https://example.com/a",
			Source:    "hackernews",
			Points:    150,
			Author:    "alice",
			CreatedAt: time.Date(2026, 4, 18, 8, 0, 0, 0, time.UTC),
			Tags:      []string{},
		},
		{
			ID:        "hn-43",
			Title:     "Another story",
			URL:       "https://example.com/b",
			Source:    "hackernews",
			Points:    90,
			Author:    "bob",
			CreatedAt: time.Date(2026, 4, 18, 7, 30, 0, 0, time.UTC),
			Tags:      []string{},
		},
	}
	if !reflect.DeepEqual(stories, want) {
		t.Errorf("stories mismatch\ngot:  %+v\nwant: %+v", stories, want)
	}

	if gotURL == nil {
		t.Fatal("server received no request")
	}
	if got, want := gotURL.Path, "/search_by_date"; got != want {
		t.Errorf("request path = %q, want %q", got, want)
	}
	q := gotURL.Query()
	if got, want := q.Get("tags"), "story"; got != want {
		t.Errorf("tags = %q, want %q", got, want)
	}
	if got, want := q.Get("numericFilters"), "points>=50"; got != want {
		t.Errorf("numericFilters = %q, want %q", got, want)
	}
	if got, want := q.Get("hitsPerPage"), "30"; got != want {
		t.Errorf("hitsPerPage = %q, want %q", got, want)
	}
}

func TestHackerNews_Fetch_EmptyHits(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"hits":[]}`)
	}))
	defer ts.Close()

	h := &fetch.HackerNews{BaseURL: ts.URL, Client: ts.Client()}
	stories, err := h.Fetch(context.Background(), fetch.FetchOptions{MinPoints: 50, Limit: 30})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if stories == nil {
		t.Fatal("Fetch returned nil slice; expected empty non-nil slice")
	}
	if len(stories) != 0 {
		t.Errorf("got %d stories, want 0", len(stories))
	}
}

func TestHackerNews_Fetch_StoryWithoutURL(t *testing.T) {
	body := `{
		"hits": [
			{
				"objectID": "123",
				"title": "Ask HN: something",
				"url": "",
				"points": 60,
				"author": "carol",
				"created_at": "2026-04-18T06:00:00Z"
			}
		]
	}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer ts.Close()

	h := &fetch.HackerNews{BaseURL: ts.URL, Client: ts.Client()}
	stories, err := h.Fetch(context.Background(), fetch.FetchOptions{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(stories) != 1 {
		t.Fatalf("got %d stories, want 1", len(stories))
	}
	if got, want := stories[0].URL, "https://news.ycombinator.com/item?id=123"; got != want {
		t.Errorf("URL fallback = %q, want %q", got, want)
	}
}

func TestHackerNews_Fetch_ServerErrors(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "non-200 status",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "boom", http.StatusInternalServerError)
			},
		},
		{
			name: "malformed json",
			handler: func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `{not json`)
			},
		},
		{
			name: "unparseable created_at",
			handler: func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `{"hits":[{"objectID":"1","title":"x","url":"https://x","points":1,"author":"a","created_at":"not-a-date"}]}`)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(tc.handler)
			defer ts.Close()
			h := &fetch.HackerNews{BaseURL: ts.URL, Client: ts.Client()}
			stories, err := h.Fetch(context.Background(), fetch.FetchOptions{})
			if err == nil {
				t.Fatalf("Fetch: want error, got stories=%v", stories)
			}
			if stories != nil {
				t.Errorf("Fetch returned partial stories with error: %v", stories)
			}
		})
	}
}

func TestHackerNews_Fetch_RespectsContextCancel(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the request is cancelled client-side. We don't expect
		// to finish this handler; the test cancels before Fetch is called.
		<-r.Context().Done()
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before calling

	h := &fetch.HackerNews{BaseURL: ts.URL, Client: ts.Client()}
	_, err := h.Fetch(ctx, fetch.FetchOptions{})
	if err == nil {
		t.Fatal("Fetch: want error on cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want errors.Is(..., context.Canceled)", err)
	}
}
