package fetch

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"
)

// feedItem is one parsed RSS/Atom entry, decoupled from Story so the
// parser stays a pure document→data function; the Following fetcher maps
// items to Stories with feed-level context (feed URL, fetch time).
type feedItem struct {
	Title     string
	URL       string
	Summary   string
	Author    string
	Tags      []string
	Published time.Time
	HasDate   bool
}

type rssDoc struct {
	XMLName xml.Name  `xml:"rss"`
	Items   []rssItem `xml:"channel>item"`
}

type rssItem struct {
	Title       string   `xml:"title"`
	Link        string   `xml:"link"`
	Description string   `xml:"description"`
	PubDate     string   `xml:"pubDate"`
	Categories  []string `xml:"category"`
	Creator     string   `xml:"creator"` // dc:creator matches by local name
	Author      string   `xml:"author"`
}

type atomDoc struct {
	XMLName xml.Name    `xml:"feed"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	Title      string     `xml:"title"`
	Links      []atomLink `xml:"link"`
	Summary    string     `xml:"summary"`
	Content    string     `xml:"content"`
	Published  string     `xml:"published"`
	Updated    string     `xml:"updated"`
	Categories []atomCat  `xml:"category"`
	Author     atomPerson `xml:"author"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
}

type atomCat struct {
	Term string `xml:"term,attr"`
}

type atomPerson struct {
	Name string `xml:"name"`
}

// parseFeed parses an RSS 2.0 or Atom document. UTF-8 (with or without
// BOM) only — a non-UTF-8 encoding declaration errors, per the locked
// decision (14/14 surveyed feeds were UTF-8; revisit on a real report,
// same posture as RDF). Items missing a title or URL are silently
// dropped; an unparseable date keeps the item with HasDate=false so the
// caller can substitute fetch time. Malformed XML fails the whole feed —
// no partial salvage.
func parseFeed(data []byte) ([]feedItem, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))

	var rss rssDoc
	if err := decodeStrictUTF8(data, &rss); err == nil && rss.XMLName.Local == "rss" {
		items := make([]feedItem, 0, len(rss.Items))
		for _, it := range rss.Items {
			author := it.Creator
			if author == "" {
				author = it.Author
			}
			item, ok := buildItem(it.Title, it.Link, it.Description, author, it.Categories, it.PubDate)
			if ok {
				items = append(items, item)
			}
		}
		return items, nil
	}

	var atom atomDoc
	if err := decodeStrictUTF8(data, &atom); err == nil && atom.XMLName.Local == "feed" {
		items := make([]feedItem, 0, len(atom.Entries))
		for _, e := range atom.Entries {
			date := e.Published
			if date == "" {
				date = e.Updated
			}
			summary := e.Summary
			if summary == "" {
				summary = e.Content
			}
			tags := make([]string, 0, len(e.Categories))
			for _, c := range e.Categories {
				if c.Term != "" {
					tags = append(tags, c.Term)
				}
			}
			item, ok := buildItem(e.Title, atomAlternate(e.Links), summary, e.Author.Name, tags, date)
			if ok {
				items = append(items, item)
			}
		}
		return items, nil
	}

	// Neither decode produced a recognised root: report the underlying
	// XML error if there was one, else the format mismatch.
	if err := decodeStrictUTF8(data, &rss); err != nil {
		return nil, fmt.Errorf("parse feed: %w", err)
	}
	return nil, errors.New("parse feed: document is neither RSS 2.0 nor Atom")
}

// decodeStrictUTF8 unmarshals without a CharsetReader, so any non-UTF-8
// encoding declaration surfaces as an error rather than mojibake.
func decodeStrictUTF8(data []byte, v any) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	return dec.Decode(v)
}

// buildItem applies the extraction contract: title and URL required
// (else drop), summary HTML-stripped and entity-decoded, date
// best-effort.
func buildItem(title, link, summary, author string, tags []string, date string) (feedItem, bool) {
	title = strings.TrimSpace(title)
	link = strings.TrimSpace(link)
	if title == "" || link == "" {
		return feedItem{}, false
	}
	item := feedItem{
		Title:   title,
		URL:     link,
		Summary: stripTags(summary),
		Author:  strings.TrimSpace(author),
		Tags:    tags,
	}
	// RSS 2.0 mandates RFC 822-ish dates but the wild mixes named
	// zones, four-digit years, and stray RFC 3339 (survey: pubDate was
	// populated in 14/14 feeds, formats varied). Function-local per the
	// no-package-level-mutable-state rule; off the render hot path.
	layouts := []string{
		time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822, time.RFC3339,
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, strings.TrimSpace(date)); err == nil {
			item.Published = t.UTC()
			item.HasDate = true
			break
		}
	}
	return item, true
}

// atomAlternate picks the entry's canonical link: rel="alternate" or an
// unqualified rel wins; rel="self"/"edit"/etc. are feed plumbing.
func atomAlternate(links []atomLink) string {
	for _, l := range links {
		if l.Rel == "alternate" && l.Href != "" {
			return l.Href
		}
	}
	for _, l := range links {
		if l.Rel == "" && l.Href != "" {
			return l.Href
		}
	}
	return ""
}

// stripTags removes HTML tags from summary text, decodes HTML entities,
// and collapses the whitespace tags leave behind. The summary is a
// topic-matching surface, never rendered — matching against raw markup
// would false-match on attribute text, so a cheap tag strip beats a real
// HTML parser here.
//
// A '<' opens a tag only when followed by a letter, '/', '!', or '?' —
// the HTML5 tokenizer's rule. The XML decoder has already turned "&lt;"
// into a literal '<', so plain-text comparisons ("2 < 3") reach this
// function as bare angle brackets and must survive; without the check
// the stripper would eat everything from the '<' onward. A '>' outside
// a tag is likewise literal text. Entities decode AFTER the strip
// (CDATA-wrapped descriptions reach the XML decoder raw, so "&amp;" is
// still literal here).
func stripTags(s string) string {
	var b strings.Builder
	inTag := false
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case inTag:
			if r == '>' {
				inTag = false
				b.WriteRune(' ')
			}
		case r == '<' && i+1 < len(runes) && isTagStart(runes[i+1]):
			inTag = true
		default:
			b.WriteRune(r)
		}
	}
	return html.UnescapeString(strings.Join(strings.Fields(b.String()), " "))
}

// isTagStart reports whether r can begin a tag name after '<' (HTML5:
// ASCII letter, or the '/', '!', '?' markers for close tags, comments/
// doctypes, and processing instructions).
func isTagStart(r rune) bool {
	return r == '/' || r == '!' || r == '?' ||
		(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}
