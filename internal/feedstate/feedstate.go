// Package feedstate reads and writes the feed fetch state (feeds.json).
//
// Two things need to survive between runs, per configured feed: the HTTP
// conditional-GET validators (ETag / Last-Modified), so a fetch that finds
// nothing new costs a 304 instead of a full body; and the publish dates of
// the items last seen there, which the cadence weighting is computed from.
// FirstSeen anchors a cold-start confidence blend so a feed newly added to
// the config isn't immediately judged dormant on a single observation.
//
// The store lives under XDG_STATE_HOME rather than XDG_CACHE_HOME because
// it is not a rebuildable derived artefact: losing it loses validators
// (falling back to unconditional GETs) and the cadence history (falling
// back to neutral weights) rather than just a pool the next fetch
// repopulates. That puts it next to internal/history and internal/session,
// which record the same kind of durable, non-cache state.
package feedstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/lockfile"
)

// SchemaVersion identifies the on-disk layout. Bump when Feed or File
// gains or loses a field, or when an existing field changes semantics.
const SchemaVersion = 1

// cadenceWindow is the rolling window the cadence rate is computed over:
// items/week = in-window pubDate count / 4.
const cadenceWindow = 4 * 7 * 24 * time.Hour

// ErrSchemaVersion is returned by [Read] when the file declares a schema
// version other than [SchemaVersion]. Callers should treat it like any
// other corruption and fall back to an empty store.
var ErrSchemaVersion = errors.New("feedstate: unsupported schema version")

// Feed is the persisted state for one configured feed URL.
type Feed struct {
	URL       string    `json:"url"`
	FirstSeen time.Time `json:"first_seen"`
	// ObservedAt is the time of the last fetch that reached this feed,
	// 304s included. Nothing in the fetch/weighting path reads it; it is
	// recorded for Part 2's staleness reporting and diagnostics ("last
	// checked N hours ago"), which is the only way to tell a feed that is
	// quiet from one that has not been polled.
	ObservedAt time.Time   `json:"observed_at"`
	PubDates   []time.Time `json:"pub_dates"` // dated items from the last fetched doc, write-pruned to the 4-week window (future dates kept)
	// SeenDated records that this feed has reported at least one dated
	// item at least once. It gates the dormant boost: a feed that has
	// never carried a parseable date has no cadence signal at all (as
	// opposed to a demonstrated cadence that went quiet), and no signal
	// means neutral, not maximum boost. Set once, never cleared.
	SeenDated    bool   `json:"seen_dated"`
	ETag         string `json:"etag"`
	LastModified string `json:"last_modified"`
}

// File is the on-disk feed-state layout. JSON tags are part of the schema
// contract.
type File struct {
	Version int    `json:"version"`
	Feeds   []Feed `json:"feeds"`
}

// Observation is one feed's result from a single fetch pass, as reported
// by the caller to [Update].
type Observation struct {
	URL          string
	PubDates     []time.Time // publish times of dated items in the fetched document
	DatesKnown   bool        // false on 304s — keep the stored dates (unchanged doc = unchanged dates)
	ETag         string
	LastModified string
}

// Path returns the absolute path to feeds.json. It honours XDG_STATE_HOME
// first, then falls back to $HOME/.local/state/newsfetch/feeds.json.
func Path() (string, error) {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "newsfetch", "feeds.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve feedstate path: %w", err)
	}
	return filepath.Join(home, ".local", "state", "newsfetch", "feeds.json"), nil
}

// Read parses the feed state at path. A missing file is not an error: it
// returns an empty File at the current SchemaVersion. Corrupt content or
// a version mismatch is returned to the caller (Update treats either as
// an empty starting state; readers should treat it as "no state").
func Read(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &File{Version: SchemaVersion}, nil
		}
		return nil, fmt.Errorf("read feeds: %w", err)
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse feeds: %w", err)
	}
	if f.Version != SchemaVersion {
		return nil, fmt.Errorf("%w: got %d, want %d", ErrSchemaVersion, f.Version, SchemaVersion)
	}
	return &f, nil
}

// Update upserts one observation per fetched feed and garbage-collects
// feeds no longer in the configured list, under an exclusive lock on a
// sidecar feeds.lock (every state-file read-modify-write in this repo
// holds lockfile.Acquire — seen.json and sessions.json set the pattern).
// A 304-style observation (DatesKnown=false) refreshes ObservedAt but
// keeps the stored pubDates and validators — an unchanged document has
// unchanged dates, and Weights re-windows them at read time. A 200
// replaces both validators outright, empty values included, so a feed
// that stops sending one is recorded as such rather than pinned to a
// stale validator forever (the fetcher already back-fills a 304's
// validators, so a real 200 is the only case reaching this branch).
// Stored dates are pruned to those newer than now−4w on every write
// (future dates kept: they start counting when the window reaches them).
// FirstSeen is set on first sight and never moves — it anchors the
// cadence confidence blend. SeenDated latches true on the first
// observation that carries a date and is never cleared.
func Update(path string, configured []string, obs []Observation, now time.Time) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create feedstate dir: %w", err)
	}
	lock, err := lockfile.Acquire(filepath.Join(dir, "feeds.lock"), time.Second)
	if err != nil {
		return err // Acquire's errors already name the lock path
	}
	defer lock.Close() // close releases the flock

	f, err := Read(path)
	if err != nil {
		f = &File{Version: SchemaVersion} // corrupt → start clean, same policy as history
	}
	byURL := make(map[string]Feed, len(f.Feeds))
	for _, fd := range f.Feeds {
		byURL[fd.URL] = fd
	}
	for _, o := range obs {
		fd, ok := byURL[o.URL]
		if !ok {
			fd = Feed{URL: o.URL, FirstSeen: now}
		}
		if o.DatesKnown {
			fd.PubDates = append([]time.Time(nil), o.PubDates...)
			if len(o.PubDates) > 0 {
				fd.SeenDated = true
			}
			// A real 200: its validators are the whole truth, including
			// the absence of one. Overwriting only non-empty values would
			// pin a validator the server has stopped sending.
			fd.ETag = o.ETag
			fd.LastModified = o.LastModified
		}
		fd.ObservedAt = now
		byURL[o.URL] = fd
	}
	keep := make(map[string]struct{}, len(configured))
	for _, u := range configured {
		keep[u] = struct{}{}
	}
	f.Feeds = f.Feeds[:0]
	for _, fd := range byURL {
		if _, ok := keep[fd.URL]; !ok {
			continue
		}
		fd.PubDates = pruneDates(fd.PubDates, now)
		f.Feeds = append(f.Feeds, fd)
	}
	sort.Slice(f.Feeds, func(i, j int) bool { return f.Feeds[i].URL < f.Feeds[j].URL })
	f.Version = SchemaVersion

	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("encode feeds: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "feeds-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp feeds: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp feeds: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp feeds: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp feeds: %w", err)
	}
	return nil
}

// pruneDates keeps dates newer than now−cadenceWindow. Future dates are
// kept: pruning them would silently delete items that become in-window
// later, when the intervening fetches may all be 304s.
func pruneDates(dates []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-cadenceWindow)
	out := make([]time.Time, 0, len(dates))
	for _, d := range dates {
		if d.After(cutoff) {
			out = append(out, d)
		}
	}
	return out
}

// Validators returns the stored conditional-GET pair for url; empty
// strings on miss.
func (f *File) Validators(url string) (etag, lastModified string) {
	for _, fd := range f.Feeds {
		if fd.URL == url {
			return fd.ETag, fd.LastModified
		}
	}
	return "", ""
}

// Weights returns the cadence multiplier per configured feed URL,
// recomputed from stored pubDates at read time so the rolling window
// keeps rolling across 304s (addendum §12). rate = count of dates in
// (now−4w, now] ÷ 4. The corpus reference is the MEDIAN rate across
// configured feeds that have observations, dormant zeros included; a
// zero median falls back to the median of the nonzero rates, and if
// every rate is zero there is no signal and every weight is neutral 1.0
// (addendum §13). computed = median/rate, with rate 0 (dormant) taking
// the capped 5.0 instead of dividing by zero, and computed capped at
// 5.0 before the blend. Cold start blends toward neutral: confidence =
// clamp(age/4wk, 0, 1); w = conf*computed + (1-conf)*1.0 — so the
// final weight sits in (0, 5.0] by construction, the same bound the
// config gives manual weights.
//
// A feed with no observation is neutral, and so is a feed that has never
// once reported a dated item (!SeenDated): a document whose dates are
// all unparseable yields the same empty pubDates as a genuinely quiet
// feed, but it carries NO cadence signal rather than a demonstrated
// cadence that stopped — and every undated item also takes fetch time as
// its timestamp, so reading that as dormancy would hand one malformed
// feed max boost × max recency on every render, forever. Such feeds are
// left out of the corpus median too, exactly like an unobserved feed: a
// rate that was never reported is not a zero to average in. The dormant
// 5.0 stays for feeds that showed a cadence and went quiet.
func (f *File) Weights(configured []string, now time.Time) map[string]float64 {
	byURL := make(map[string]Feed, len(f.Feeds))
	for _, fd := range f.Feeds {
		byURL[fd.URL] = fd
	}
	rateOf := func(fd Feed) float64 {
		cutoff := now.Add(-cadenceWindow)
		n := 0
		for _, d := range fd.PubDates {
			if d.After(cutoff) && !d.After(now) {
				n++
			}
		}
		return float64(n) / 4.0
	}
	rates := make([]float64, 0, len(configured))
	for _, u := range configured {
		if fd, ok := byURL[u]; ok && fd.SeenDated {
			rates = append(rates, rateOf(fd))
		}
	}
	med := median(rates)
	if med == 0 {
		med = median(nonzero(rates))
	}
	out := make(map[string]float64, len(configured))
	for _, u := range configured {
		fd, ok := byURL[u]
		if !ok || !fd.SeenDated || med == 0 {
			out[u] = 1.0
			continue
		}
		computed := 5.0 // dormant: no in-window items → max boost
		if rate := rateOf(fd); rate > 0 {
			computed = med / rate
			if computed > 5.0 {
				computed = 5.0
			}
		}
		confidence := now.Sub(fd.FirstSeen).Hours() / cadenceWindow.Hours()
		if confidence < 0 {
			confidence = 0
		}
		if confidence > 1 {
			confidence = 1
		}
		out[u] = confidence*computed + (1 - confidence)
	}
	return out
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if n := len(s); n%2 == 1 {
		return s[n/2]
	} else {
		return (s[n/2-1] + s[n/2]) / 2
	}
}

func nonzero(xs []float64) []float64 {
	out := make([]float64, 0, len(xs))
	for _, x := range xs {
		if x > 0 {
			out = append(out, x)
		}
	}
	return out
}
