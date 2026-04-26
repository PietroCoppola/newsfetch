package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func newLobstersTestServer(t *testing.T, status int, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func TestLobsters_Fetch_DropsEmptyTitleStories(t *testing.T) {
	srv := newLobstersTestServer(t, http.StatusOK, loadFixture(t, "lobsters_hottest.json"))
	l := &Lobsters{URL: srv.URL}

	stories, err := l.Fetch(context.Background(), FetchOptions{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// Fixture has 5 items; one (short_id=ask123) has empty title and must
	// be silently dropped.
	if len(stories) != 4 {
		t.Errorf("returned %d stories, want 4 (empty-title item should be dropped)", len(stories))
	}
	for _, s := range stories {
		if s.ID == "lobsters-ask123" {
			t.Errorf("empty-title story (short_id=ask123) was not dropped")
		}
		if s.Title == "" {
			t.Errorf("story %q has empty title; should have been filtered", s.ID)
		}
	}
}

func TestLobsters_Fetch_FallsBackToShortIDURL(t *testing.T) {
	srv := newLobstersTestServer(t, http.StatusOK, loadFixture(t, "lobsters_hottest.json"))
	l := &Lobsters{URL: srv.URL}

	stories, err := l.Fetch(context.Background(), FetchOptions{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// Fixture short_id=txt789 has empty url and a non-empty short_id_url.
	var found *Story
	for i := range stories {
		if stories[i].ID == "lobsters-txt789" {
			found = &stories[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected story lobsters-txt789 in results")
	}
	if found.URL != "https://lobste.rs/s/txt789" {
		t.Errorf("URL fallback failed: got %q, want %q", found.URL, "https://lobste.rs/s/txt789")
	}
}

func TestLobsters_Fetch_FieldMapping(t *testing.T) {
	srv := newLobstersTestServer(t, http.StatusOK, loadFixture(t, "lobsters_hottest.json"))
	l := &Lobsters{URL: srv.URL}

	stories, err := l.Fetch(context.Background(), FetchOptions{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// Spot-check the first story (abc123 / "Rust 1.87 lands") for full
	// field-mapping correctness.
	var s *Story
	for i := range stories {
		if stories[i].ID == "lobsters-abc123" {
			s = &stories[i]
			break
		}
	}
	if s == nil {
		t.Fatal("expected story lobsters-abc123 in results")
	}
	if s.Title != "Rust 1.87 lands" {
		t.Errorf("Title = %q, want %q", s.Title, "Rust 1.87 lands")
	}
	if s.URL != "https://blog.rust-lang.org/2026/04/25/Rust-1.87.html" {
		t.Errorf("URL = %q", s.URL)
	}
	if s.Source != "lobsters" {
		t.Errorf("Source = %q, want %q", s.Source, "lobsters")
	}
	if s.Points != 87 {
		t.Errorf("Points = %d, want 87", s.Points)
	}
	if s.Author != "alice" {
		t.Errorf("Author = %q, want %q", s.Author, "alice")
	}
	if len(s.Tags) != 1 || s.Tags[0] != "rust" {
		t.Errorf("Tags = %v, want [rust]", s.Tags)
	}
	if s.CreatedAt.IsZero() {
		t.Errorf("CreatedAt is zero")
	}
}

func TestLobsters_Fetch_HTTPErrorReturnsError(t *testing.T) {
	srv := newLobstersTestServer(t, http.StatusServiceUnavailable, []byte("upstream broken"))
	l := &Lobsters{URL: srv.URL}

	_, err := l.Fetch(context.Background(), FetchOptions{})
	if err == nil {
		t.Fatal("expected error for 503 response")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error should mention status code; got: %v", err)
	}
}

func TestLobsters_Fetch_MalformedJSONReturnsError(t *testing.T) {
	srv := newLobstersTestServer(t, http.StatusOK, []byte("{ not valid json"))
	l := &Lobsters{URL: srv.URL}

	_, err := l.Fetch(context.Background(), FetchOptions{})
	if err == nil {
		t.Fatal("expected decode error")
	}
}

func TestLobsters_Fetch_SendsUserAgent(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(srv.Close)
	l := &Lobsters{URL: srv.URL}

	if _, err := l.Fetch(context.Background(), FetchOptions{}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.HasPrefix(gotUA, "newsfetch/") {
		t.Errorf("User-Agent = %q, want prefix newsfetch/", gotUA)
	}
	if !strings.Contains(gotUA, "github.com/PietroCoppola/newsfetch") {
		t.Errorf("User-Agent should contain repo URL; got: %q", gotUA)
	}
}

func TestLobsters_Name(t *testing.T) {
	if (&Lobsters{}).Name() != "lobsters" {
		t.Error("Name() should return \"lobsters\"")
	}
}
