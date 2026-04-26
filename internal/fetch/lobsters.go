package fetch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// LobstersHottestURL is the JSON endpoint for Lobste.rs' hottest list.
const LobstersHottestURL = "https://lobste.rs/hottest.json"

// Lobsters implements [Source] against Lobste.rs' hottest.json. The zero
// value uses LobstersHottestURL and [http.DefaultClient]; tests can
// override URL and Client.
type Lobsters struct {
	URL    string
	Client *http.Client
}

// Name returns the stable identifier for this source ("lobsters").
func (l *Lobsters) Name() string { return "lobsters" }

// Fetch retrieves Lobste.rs' hottest list. opts.MinPoints is ignored — see
// [FetchOptions.MinPoints]: Lobste.rs is curated and its hottest list has
// no per-call points-floor knob. opts.Limit is also ignored: the endpoint
// returns ~25 items unconditionally.
//
// Empty-title items are dropped silently. Lobste.rs allows text-only "ask"
// submissions where both title and url are empty; they're discussion
// threads rather than bite-sized news, so newsfetch isn't the right
// surface for them. URL-only-empty items (title present, url absent) fall
// back to the Lobste.rs comments page.
func (l *Lobsters) Fetch(ctx context.Context, _ FetchOptions) ([]Story, error) {
	target := l.URL
	if target == "" {
		target = LobstersHottestURL
	}
	client := l.Client
	if client == nil {
		client = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent())

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("lobsters request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lobsters request: status %d", resp.StatusCode)
	}

	var raw []struct {
		ShortID    string   `json:"short_id"`
		Title      string   `json:"title"`
		URL        string   `json:"url"`
		ShortIDURL string   `json:"short_id_url"`
		Score      int      `json:"score"`
		Submitter  string   `json:"submitter_user"`
		CreatedAt  string   `json:"created_at"`
		Tags       []string `json:"tags"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode lobsters response: %w", err)
	}

	stories := make([]Story, 0, len(raw))
	for _, item := range raw {
		if item.Title == "" {
			continue
		}
		createdAt, err := time.Parse(time.RFC3339, item.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("parse created_at %q: %w", item.CreatedAt, err)
		}
		storyURL := item.URL
		if storyURL == "" {
			storyURL = item.ShortIDURL
		}
		tags := item.Tags
		if tags == nil {
			tags = []string{}
		}
		stories = append(stories, Story{
			ID:        "lobsters-" + item.ShortID,
			Title:     item.Title,
			URL:       storyURL,
			Source:    "lobsters",
			Points:    item.Score,
			Author:    item.Submitter,
			CreatedAt: createdAt.UTC(),
			Tags:      tags,
		})
	}
	return stories, nil
}
