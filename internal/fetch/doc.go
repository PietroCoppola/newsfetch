// Package fetch defines the [Source] interface and its implementations for
// retrieving stories from upstream news providers.
//
// # Source contract
//
// A Source is a stateless client for a single upstream provider (Hacker News
// via Algolia in v1; Lobste.rs, RSS, and GitHub Trending are planned for later
// milestones). Implementations must follow these rules:
//
//   - Constructors do no I/O. All network access happens inside Fetch.
//   - Fetch honours the context's deadline and cancellation. Callers rely on
//     this to bound the background refresh and keep the shell prompt snappy.
//   - Fetch does not retry internally. Retry policy, if any, belongs to the
//     caller.
//   - On transport or parse failure, Fetch returns (nil, err) — never a
//     partial slice with a non-nil error. Callers rely on this invariant to
//     keep the offline fallback honest.
//   - Stories are returned in whatever order the upstream naturally produces
//     (for example, HN Algolia's search_by_date returns newest first). The
//     ranker (M2) is responsible for re-ordering.
//   - Name returns a short, stable identifier (such as "hackernews") that is
//     safe to embed in cache entries and user-visible output.
//
// # Hot-path discipline
//
// Implementations may depend on net/http, encoding/json, and the stdlib time
// package. Any additional third-party dependency needs justification: a
// stdlib alternative considered and ruled out, with the reason recorded in
// the commit message. The fetcher runs off the render hot path, but binary
// size still affects startup cost.
package fetch
