package fetch

import (
	"net/url"
	"strings"
)

// Hash returns a stable dedup key for s. Two stories that point at the same
// underlying article should produce the same Hash even when the URLs differ
// in tracking parameters, host casing, or www./m. subdomain prefixes.
//
// The key is the normalised URL itself, not a cryptographic hash. Bounded at
// 500 entries by the history file, the storage cost is negligible and being
// able to read the dedup key directly out of seen.json is worth more than
// the compaction. If real-world URLs turn out uncomfortably long, switch to
// truncated SHA-256 here — the change is invisible to callers.
//
// Normalisation rules:
//   - Lowercase the host.
//   - Strip a leading "www." or "m." from the host.
//   - Drop the query string entirely. Most sources put article identity in
//     the path; query parameters are usually tracking junk that varies
//     between submissions of the same article.
//
// Fallback: when URL is empty (or any other parse-or-normalise failure), the
// key is "<source>:<id>". This handles Lobste.rs "ask" submissions whose
// canonical URL is the discussion page rather than an external link, and
// any future source that produces title-only items.
//
// URL-shape helpers live in this package because [Story] does. They are not
// a general-purpose URL toolkit — additions here should stay tied to Story
// semantics rather than accreting unrelated string utilities.
func (s Story) Hash() string {
	if s.URL == "" {
		return s.Source + ":" + s.ID
	}
	u, err := url.Parse(s.URL)
	if err != nil || u.Host == "" {
		return s.Source + ":" + s.ID
	}
	host := strings.ToLower(u.Host)
	host = strings.TrimPrefix(host, "www.")
	host = strings.TrimPrefix(host, "m.")
	scheme := u.Scheme
	if scheme == "" {
		scheme = "https"
	}
	return scheme + "://" + host + u.Path
}

// NormalisedHost returns the lowercase host of s.URL with leading "www." and
// "m." stripped. It returns the empty string when the URL is empty or
// unparseable. Callers needing same-publication comparisons (the diversity
// penalty, the boxed render's hostname display) should route through this
// function so the normalisation rule stays in one place.
func (s Story) NormalisedHost() string {
	if s.URL == "" {
		return ""
	}
	u, err := url.Parse(s.URL)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Host)
	host = strings.TrimPrefix(host, "www.")
	host = strings.TrimPrefix(host, "m.")
	return host
}
