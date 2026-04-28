package fetch_test

import (
	"testing"

	"github.com/PietroCoppola/newsfetch/internal/fetch"
)

func TestStory_Hash(t *testing.T) {
	cases := []struct {
		name string
		in   fetch.Story
		want string
	}{
		{
			name: "plain url",
			in:   fetch.Story{URL: "https://example.com/article", Source: "hackernews", ID: "1"},
			want: "https://example.com/article",
		},
		{
			name: "strips query string",
			in:   fetch.Story{URL: "https://example.com/article?utm_source=hn&utm_medium=feed", Source: "hackernews", ID: "1"},
			want: "https://example.com/article",
		},
		{
			name: "lowercases host",
			in:   fetch.Story{URL: "https://Example.COM/article", Source: "hackernews", ID: "1"},
			want: "https://example.com/article",
		},
		{
			name: "strips www prefix",
			in:   fetch.Story{URL: "https://www.example.com/article", Source: "hackernews", ID: "1"},
			want: "https://example.com/article",
		},
		{
			name: "strips m prefix",
			in:   fetch.Story{URL: "https://m.example.com/article", Source: "hackernews", ID: "1"},
			want: "https://example.com/article",
		},
		{
			name: "preserves http scheme",
			in:   fetch.Story{URL: "http://example.com/article", Source: "hackernews", ID: "1"},
			want: "http://example.com/article",
		},
		{
			name: "empty url falls back to source:id",
			in:   fetch.Story{URL: "", Source: "lobsters", ID: "abc123"},
			want: "lobsters:abc123",
		},
		{
			name: "unparseable url falls back",
			in:   fetch.Story{URL: "://broken", Source: "lobsters", ID: "xyz"},
			want: "lobsters:xyz",
		},
		{
			name: "url with no host falls back",
			in:   fetch.Story{URL: "/path/only", Source: "hackernews", ID: "42"},
			want: "hackernews:42",
		},
		{
			name: "trailing slash preserved (path is identity)",
			in:   fetch.Story{URL: "https://example.com/article/", Source: "hackernews", ID: "1"},
			want: "https://example.com/article/",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.Hash(); got != tc.want {
				t.Errorf("Hash = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStory_Hash_DedupAcrossUTMVariants(t *testing.T) {
	// The whole point of normalisation: two submissions of the same article
	// with different tracking params should hash identically.
	a := fetch.Story{URL: "https://example.com/x?utm_source=hn"}
	b := fetch.Story{URL: "https://example.com/x?utm_source=lobsters&ref=feed"}
	c := fetch.Story{URL: "https://www.example.com/x"}
	if a.Hash() != b.Hash() || b.Hash() != c.Hash() {
		t.Errorf("expected equal hashes, got:\n  a=%q\n  b=%q\n  c=%q", a.Hash(), b.Hash(), c.Hash())
	}
}

func TestStory_NormalisedHost(t *testing.T) {
	cases := []struct {
		name string
		in   fetch.Story
		want string
	}{
		{name: "plain", in: fetch.Story{URL: "https://example.com/x"}, want: "example.com"},
		{name: "www stripped", in: fetch.Story{URL: "https://www.example.com/x"}, want: "example.com"},
		{name: "m stripped", in: fetch.Story{URL: "https://m.example.com/x"}, want: "example.com"},
		{name: "case lowered", in: fetch.Story{URL: "https://Example.COM/x"}, want: "example.com"},
		{name: "empty url", in: fetch.Story{URL: ""}, want: ""},
		{name: "unparseable", in: fetch.Story{URL: "://x"}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.NormalisedHost(); got != tc.want {
				t.Errorf("NormalisedHost = %q, want %q", got, tc.want)
			}
		})
	}
}
