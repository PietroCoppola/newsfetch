package render

import (
	"encoding/json"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/fetch"
)

// JSON renders s as a single-line JSON object per spec §10:
//
//	{"title":"...","url":"...","source":"...","age_seconds":N,"tags":[]}
//
// No error return: json.Marshal on a struct of scalars and a string slice
// does not fail in practice. Trailing newline is intentional for shell
// pipelines.
func JSON(s fetch.Story, now time.Time) string {
	type payload struct {
		Title      string   `json:"title"`
		URL        string   `json:"url"`
		Source     string   `json:"source"`
		AgeSeconds int64    `json:"age_seconds"`
		Tags       []string `json:"tags"`
	}
	// Normalize nil → empty slice so the wire form is always "tags":[].
	tags := s.Tags
	if tags == nil {
		tags = []string{}
	}
	// Clamp negative age to 0 to match rank.Score's handling of clock skew.
	ageSeconds := int64(now.Sub(s.CreatedAt).Seconds())
	if ageSeconds < 0 {
		ageSeconds = 0
	}
	b, _ := json.Marshal(payload{
		Title:      s.Title,
		URL:        s.URL,
		Source:     s.Source,
		AgeSeconds: ageSeconds,
		Tags:       tags,
	})
	return string(b) + "\n"
}
