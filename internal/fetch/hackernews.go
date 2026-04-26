package fetch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// HackerNewsAlgoliaURL is the root of the Algolia-powered HN search API.
const HackerNewsAlgoliaURL = "https://hn.algolia.com/api/v1"

// HackerNews implements [Source] against the Algolia search_by_date endpoint.
// The zero value uses HackerNewsAlgoliaURL and [http.DefaultClient]; tests
// can override BaseURL and Client.
type HackerNews struct {
	BaseURL string
	Client  *http.Client
}

// Name returns the stable identifier for this source ("hackernews").
func (h *HackerNews) Name() string { return "hackernews" }

// Fetch retrieves recent stories from HN via search_by_date. See package doc
// for the full contract.
func (h *HackerNews) Fetch(ctx context.Context, opts FetchOptions) ([]Story, error) {
	base := h.BaseURL
	if base == "" {
		base = HackerNewsAlgoliaURL
	}
	client := h.Client
	if client == nil {
		client = http.DefaultClient
	}

	u, err := url.Parse(base + "/search_by_date")
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}
	q := u.Query()
	q.Set("tags", "story")
	if opts.MinPoints > 0 {
		q.Set("numericFilters", "points>="+strconv.Itoa(opts.MinPoints))
	}
	if opts.Limit > 0 {
		q.Set("hitsPerPage", strconv.Itoa(opts.Limit))
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent())
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hn request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hn request: status %d", resp.StatusCode)
	}

	var payload struct {
		Hits []struct {
			ObjectID  string `json:"objectID"`
			Title     string `json:"title"`
			URL       string `json:"url"`
			Points    int    `json:"points"`
			Author    string `json:"author"`
			CreatedAt string `json:"created_at"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode hn response: %w", err)
	}

	stories := make([]Story, 0, len(payload.Hits))
	for _, hit := range payload.Hits {
		createdAt, err := time.Parse(time.RFC3339, hit.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("parse created_at %q: %w", hit.CreatedAt, err)
		}
		storyURL := hit.URL
		if storyURL == "" {
			storyURL = "https://news.ycombinator.com/item?id=" + hit.ObjectID
		}
		stories = append(stories, Story{
			ID:        "hn-" + hit.ObjectID,
			Title:     hit.Title,
			URL:       storyURL,
			Source:    "hackernews",
			Points:    hit.Points,
			Author:    hit.Author,
			CreatedAt: createdAt.UTC(),
			Tags:      []string{},
		})
	}
	return stories, nil
}
