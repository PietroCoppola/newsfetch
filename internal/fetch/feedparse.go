package fetch

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
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
	Titles       []string `xml:"title"`
	Links        []string `xml:"link"`
	Descriptions []string `xml:"description"`
	PubDates     []string `xml:"pubDate"`
	Categories   []string `xml:"category"`
	Creators     []string `xml:"creator"` // dc:creator matches by local name
	Authors      []string `xml:"author"`
}

type atomDoc struct {
	XMLName xml.Name    `xml:"feed"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	Titles     []string     `xml:"title"`
	Links      []atomLink   `xml:"link"`
	Summaries  []atomText   `xml:"summary"`
	Contents   []atomText   `xml:"content"`
	Published  []string     `xml:"published"`
	Updated    []string     `xml:"updated"`
	Categories []atomCat    `xml:"category"`
	Authors    []atomPerson `xml:"author"`
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

// atomText is an Atom text construct (<summary>, <content>). Chardata
// carries the text for type="text" and type="html" — the decoder has
// already resolved entities and CDATA there — while type="xhtml" puts the
// text inside child elements, where chardata sees nothing and only the raw
// Inner markup has it.
type atomText struct {
	Chardata string `xml:",chardata"`
	Inner    string `xml:",innerxml"`
}

// parseFeed parses an RSS 2.0 or Atom document. UTF-8 (with or without
// BOM) only — a non-UTF-8 encoding declaration errors, per the locked
// decision (14/14 surveyed feeds were UTF-8; revisit on a real report,
// same posture as RDF). Items missing a title or URL are silently
// dropped; an unparseable date keeps the item with HasDate=false so the
// caller can substitute fetch time. Malformed XML fails the whole feed —
// no partial salvage, and that includes anything outside XML's legal
// prolog before the root element (see [readPrologue]) or its Misc*
// epilogue after it (see [checkEpilogue]). The root element name is
// examined once to dispatch to the correct format parser.
//
// One rule governs every text field on both paths: decode it as a SLICE
// and take the first non-empty trimmed value. encoding/xml matches child
// elements by local name only, so a namespaced sibling — <atom:link
// rel="self"> inside an RSS <item> (Blogger, Atom→RSS bridges),
// <dc:title>, <media:description>, <media:title> inside an Atom <entry> —
// lands in the same field as the real element. A scalar field would take
// whichever came LAST, so a foreign empty element would blank a required
// field and silently drop the whole item, with the outcome depending on
// element order. Extend this parser the same way: never a bare scalar.
func parseFeed(data []byte) ([]feedItem, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))

	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true

	rootStart, err := readPrologue(dec)
	if err != nil {
		return nil, err
	}

	switch rootStart.Name.Local {
	case "rss":
		var rss rssDoc
		if err := dec.DecodeElement(&rss, &rootStart); err != nil {
			return nil, fmt.Errorf("parse feed: %w", err)
		}
		items := make([]feedItem, 0, len(rss.Items))
		for _, it := range rss.Items {
			author := firstNonEmpty(it.Creators)
			if author == "" {
				author = firstNonEmpty(it.Authors)
			}
			item, ok := buildItem(
				firstNonEmpty(it.Titles),
				firstNonEmpty(it.Links),
				firstNonEmpty(it.Descriptions),
				author,
				it.Categories,
				firstNonEmpty(it.PubDates),
			)
			if ok {
				items = append(items, item)
			}
		}
		if err := checkEpilogue(dec); err != nil {
			return nil, err
		}
		return items, nil

	case "feed":
		var atom atomDoc
		if err := dec.DecodeElement(&atom, &rootStart); err != nil {
			return nil, fmt.Errorf("parse feed: %w", err)
		}
		items := make([]feedItem, 0, len(atom.Entries))
		for _, e := range atom.Entries {
			date := firstNonEmpty(e.Published)
			if date == "" {
				date = firstNonEmpty(e.Updated)
			}
			summary := firstNonEmptyText(e.Summaries)
			if summary == "" {
				summary = firstNonEmptyText(e.Contents)
			}
			tags := make([]string, 0, len(e.Categories))
			for _, c := range e.Categories {
				tags = append(tags, c.Term)
			}
			author := ""
			for _, a := range e.Authors {
				if author = strings.TrimSpace(a.Name); author != "" {
					break
				}
			}
			item, ok := buildItem(firstNonEmpty(e.Titles), atomAlternate(e.Links), summary, author, tags, date)
			if ok {
				items = append(items, item)
			}
		}
		if err := checkEpilogue(dec); err != nil {
			return nil, err
		}
		return items, nil

	default:
		return nil, errors.New("parse feed: document is neither RSS 2.0 nor Atom")
	}
}

// readPrologue advances the decoder to the root StartElement and returns
// it, rejecting anything XML does not allow before a root. Skipping to the
// first StartElement unconditionally would swallow a proxy or CDN error
// page prepended to the feed body, contradicting parseFeed's "malformed
// XML fails the whole feed" contract at the head of the document the same
// way an unchecked epilogue does at the tail (see [checkEpilogue]).
//
// The prolog is `XMLDecl? Misc* (doctypedecl Misc*)?`: an XML declaration,
// comments, processing instructions, whitespace and — unlike the epilogue
// — a DOCTYPE directive. Real feeds ship all of them, so only non-
// whitespace text and a stray end element are rejected here. Ordering
// within the prolog is left to the decoder; the failure this guards
// against is content that has no business in a prolog at all.
func readPrologue(dec *xml.Decoder) (xml.StartElement, error) {
	for {
		t, err := dec.Token()
		if err != nil {
			return xml.StartElement{}, fmt.Errorf("parse feed: %w", err)
		}
		switch tok := t.(type) {
		case xml.StartElement:
			return tok, nil
		case xml.CharData:
			if strings.TrimSpace(string(tok)) != "" {
				return xml.StartElement{}, errors.New("parse feed: unexpected text before root element")
			}
		case xml.Comment, xml.ProcInst, xml.Directive:
			// legal prolog
		default:
			// An end element here is a syntax error the strict decoder
			// already reports; this keeps the set closed either way.
			return xml.StartElement{}, fmt.Errorf("parse feed: unexpected %T before root element", t)
		}
	}
}

// checkEpilogue drains the decoder past the root element to EOF and
// rejects anything XML does not allow there. DecodeElement returns as
// soon as the root subtree closes and encoding/xml never validates the
// remainder, so without this a valid root followed by garbage — or by a
// whole second document — parses clean, contradicting parseFeed's
// "malformed XML fails the whole feed" contract.
//
// Only the Misc* epilogue is legal: whitespace, comments, processing
// instructions. Rejecting the first non-EOF token outright would fail
// every feed that ends with a newline, which is most of them.
//
// A DOCTYPE directive is prolog-only Misc and is rejected here even
// though [readPrologue] accepts one, and so is the XML declaration, which
// Go surfaces as an ordinary ProcInst with target "xml" — both after a
// closed root mean a second document was concatenated onto this one.
func checkEpilogue(dec *xml.Decoder) error {
	for {
		t, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("parse feed: after root element: %w", err)
		}
		switch tok := t.(type) {
		case xml.CharData:
			if strings.TrimSpace(string(tok)) != "" {
				return errors.New("parse feed: unexpected text after root element")
			}
		case xml.ProcInst:
			if tok.Target == "xml" {
				return errors.New("parse feed: xml declaration after root element")
			}
		case xml.Comment:
			// legal epilogue
		default:
			return fmt.Errorf("parse feed: unexpected %T after root element", t)
		}
	}
}

// firstNonEmpty returns the first value in vals that is non-empty after
// trimming, or "". See [parseFeed]'s doc for why every text field is a
// slice.
func firstNonEmpty(vals []string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// firstNonEmptyText is [firstNonEmpty] for Atom text constructs,
// preferring each element's decoded character data over its raw inner
// markup (see [atomText]).
func firstNonEmptyText(vals []atomText) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v.Chardata); s != "" {
			return s
		}
		if s := strings.TrimSpace(v.Inner); s != "" {
			return s
		}
	}
	return ""
}

// buildItem applies the extraction contract: title and URL required
// (else drop), summary HTML-stripped and entity-decoded, tags trimmed
// with empties dropped, date best-effort.
func buildItem(title, link, summary, author string, tags []string, date string) (feedItem, bool) {
	title = strings.TrimSpace(title)
	link = strings.TrimSpace(link)
	if title == "" || link == "" {
		return feedItem{}, false
	}
	// Tags reach Story.Tags, --style=json, and the diversity penalty,
	// where two stories both carrying "" would count as sharing a tag.
	clean := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag = strings.TrimSpace(tag); tag != "" {
			clean = append(clean, tag)
		}
	}
	item := feedItem{
		Title:   title,
		URL:     link,
		Summary: stripTags(summary),
		Author:  strings.TrimSpace(author),
		Tags:    clean,
	}
	// RSS 2.0 mandates RFC 822-ish dates but the wild mixes named
	// zones, four-digit years, omitted seconds, RFC 822's parenthesised
	// zone comment, and stray RFC 3339 with or without a zone (survey:
	// pubDate was populated in 14/14 feeds, formats varied). Function-
	// local per the no-package-level-mutable-state rule; off the render
	// hot path.
	//
	// The named-zone layouts (RFC1123, RFC822, and the "(MST)" variant)
	// resolve an abbreviation against the LOCAL zone, so an abbreviation
	// this machine's zone doesn't define parses as UTC with that name
	// rather than failing. Accepted: a wrong-by-hours date is still a
	// better cadence signal than no date at all, and the alternative is
	// a hand-maintained abbreviation table.
	layouts := []string{
		time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822, time.RFC3339,
		"Mon, 02 Jan 2006 15:04:05 -0700 (MST)",
		"Mon, 02 Jan 2006 15:04 -0700",
		"02 Jan 2006 15:04:05 -0700",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
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
