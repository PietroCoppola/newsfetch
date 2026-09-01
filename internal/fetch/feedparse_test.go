package fetch

import (
	"testing"
	"time"
)

const rssFixture = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:dc="http://purl.org/dc/elements/1.1/">
<channel><title>Blog</title>
<item>
  <title>First post</title>
  <link>https://example.com/first</link>
  <description><![CDATA[Notes on <b>zig</b> &amp; comptime]]></description>
  <pubDate>Mon, 24 Aug 2026 10:00:00 +0000</pubDate>
  <category>zig</category><category>compilers</category>
  <dc:creator>alice</dc:creator>
</item>
<item><title>No link, dropped</title></item>
<item><link>https://example.com/no-title-dropped</link></item>
<item><title>Undated</title><link>https://example.com/undated</link></item>
</channel></rss>`

const atomFixture = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
<title>Atom Blog</title>
<entry>
  <title>Entry one</title>
  <link rel="self" href="https://example.org/self.xml"/>
  <link rel="alternate" href="https://example.org/one"/>
  <summary>plain summary</summary>
  <published>2026-08-20T09:30:00Z</published>
  <category term="go"/>
  <author><name>bob</name></author>
</entry>
<entry>
  <title>Entry two</title>
  <link href="https://example.org/two"/>
  <updated>2026-08-22T00:00:00Z</updated>
</entry>
</feed>`

func TestParseFeed_RSS(t *testing.T) {
	items, err := parseFeed([]byte(rssFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2 (title-less and link-less entries dropped)", len(items))
	}
	first := items[0]
	if first.Title != "First post" || first.URL != "https://example.com/first" {
		t.Errorf("first = %+v", first)
	}
	// CDATA content reaches the decoder raw, so the &amp; inside it is
	// literal until stripTags' html.UnescapeString pass decodes it.
	if want := "Notes on zig & comptime"; first.Summary != want {
		t.Errorf("Summary = %q, want %q (HTML stripped, entities decoded)", first.Summary, want)
	}
	if first.Author != "alice" {
		t.Errorf("Author = %q, want alice (dc:creator via local-name match)", first.Author)
	}
	if len(first.Tags) != 2 || first.Tags[0] != "zig" {
		t.Errorf("Tags = %v", first.Tags)
	}
	if !first.HasDate || !first.Published.Equal(time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("Published = %v HasDate=%v", first.Published, first.HasDate)
	}
	if items[1].HasDate {
		t.Error("undated item must report HasDate=false")
	}
}

func TestParseFeed_Atom(t *testing.T) {
	items, err := parseFeed([]byte(atomFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	if items[0].URL != "https://example.org/one" {
		t.Errorf("URL = %q, want the rel=alternate link, not rel=self", items[0].URL)
	}
	if items[0].Author != "bob" || items[0].Tags[0] != "go" {
		t.Errorf("item = %+v", items[0])
	}
	if !items[1].HasDate || !items[1].Published.Equal(time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)) {
		t.Error("updated must back-fill published when published is absent")
	}
}

func TestParseFeed_Edges(t *testing.T) {
	cases := []struct {
		name    string
		data    string
		wantErr bool
	}{
		{"BOM prefix accepted", "\xef\xbb\xbf" + rssFixture, false},
		{"non-UTF-8 declaration rejected", `<?xml version="1.0" encoding="ISO-8859-1"?><rss version="2.0"><channel></channel></rss>`, true},
		{"malformed XML rejected whole", `<rss><channel><item><title>x</title>`, true},
		{"unrecognised root rejected", `<?xml version="1.0"?><html></html>`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseFeed([]byte(tc.data))
			if (err != nil) != tc.wantErr {
				t.Errorf("parseFeed error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestParseFeed_TrailingContentAfterRoot(t *testing.T) {
	// DecodeElement stops at the end of the root subtree and encoding/xml
	// never validates what follows, so a valid root with anything bolted
	// onto it parsed clean — contradicting the "malformed XML fails the
	// whole feed" contract, and silently swallowing a second document.
	//
	// Rejecting the first non-EOF token is NOT the fix: XML's Misc*
	// epilogue legally allows whitespace, comments and processing
	// instructions after the root, and real feeds routinely end with a
	// newline.
	const root = `<rss version="2.0"><channel><item><title>t</title><link>https://e.com/x</link></item></channel></rss>`
	cases := []struct {
		name    string
		trailer string
		wantErr bool
	}{
		{"unterminated junk", `<junk`, true},
		{"a second complete root", root, true},
		{"stray text", "stray words", true},
		{"trailing whitespace and a comment", "\n  <!-- built by something -->\n", false},
		{"a processing instruction", "\n<?xml-stylesheet href=\"feed.xsl\"?>\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items, err := parseFeed([]byte(root + tc.trailer))
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseFeed error = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && len(items) != 1 {
				t.Errorf("items = %d, want the root's own item still parsed", len(items))
			}
		})
	}
}

func TestParseFeed_BOMPrefix(t *testing.T) {
	// Verify BOM prefix is stripped and parsing succeeds with correct content,
	// matching the behavior of the same fixture without BOM.
	items, err := parseFeed([]byte("\xef\xbb\xbf" + rssFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Errorf("item count = %d, want 2", len(items))
	}
	if len(items) > 0 {
		if items[0].Title != "First post" {
			t.Errorf("first item Title = %q, want First post", items[0].Title)
		}
		if items[0].URL != "https://example.com/first" {
			t.Errorf("first item URL = %q, want https://example.com/first", items[0].URL)
		}
	}
}

func TestParseFeed_EscapedAngleBracketsSurviveStrip(t *testing.T) {
	// The XML decoder turns &lt;/&gt; into literal angle brackets before
	// stripTags runs, so plain-text comparisons must not be eaten as
	// tags while real (CDATA-or-escaped) markup still strips.
	feed := `<rss version="2.0"><channel><item><title>t</title><link>https://e.com/x</link><description>proof that 2 &lt; 3 and x &gt; y holds, &lt;b&gt;bold&lt;/b&gt; stripped</description></item></channel></rss>`
	items, err := parseFeed([]byte(feed))
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
	if want := "proof that 2 < 3 and x > y holds, bold stripped"; items[0].Summary != want {
		t.Errorf("Summary = %q, want %q", items[0].Summary, want)
	}
}

func TestParseFeed_DateFormats(t *testing.T) {
	mk := func(date string) string {
		return `<rss version="2.0"><channel><item><title>t</title><link>https://e.com/x</link><pubDate>` + date + `</pubDate></item></channel></rss>`
	}
	cases := []struct {
		name, date string
		wantDate   bool
	}{
		{"RFC1123Z", "Mon, 24 Aug 2026 10:00:00 +0000", true},
		{"RFC1123 named zone", "Mon, 24 Aug 2026 10:00:00 GMT", true},
		{"RFC822Z two-digit year", "24 Aug 26 10:00 +0000", true},
		{"RFC3339 leaks into RSS", "2026-08-24T10:00:00Z", true},
		{"trailing zone comment", "Mon, 24 Aug 2026 10:00:00 -0400 (EDT)", true},
		{"seconds omitted", "Mon, 24 Aug 2026 10:00 +0000", true},
		{"no weekday, four-digit year", "24 Aug 2026 10:00:00 +0000", true},
		{"zoneless RFC3339 with T", "2026-08-24T10:00:00", true},
		{"zoneless timestamp with a space", "2026-08-24 10:00:00", true},
		{"bare date", "2026-08-24", true},
		{"garbage date -> HasDate false, item kept", "not a date", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items, err := parseFeed([]byte(mk(tc.date)))
			if err != nil || len(items) != 1 {
				t.Fatalf("items=%d err=%v", len(items), err)
			}
			if items[0].HasDate != tc.wantDate {
				t.Errorf("HasDate = %v, want %v", items[0].HasDate, tc.wantDate)
			}
		})
	}
}

func TestParseFeed_ForeignNamespaceSiblingsKeepTheItem(t *testing.T) {
	// encoding/xml matches child elements by LOCAL name, so a namespaced
	// sibling sharing a local name ("atom:link" inside an RSS <item>, which
	// Blogger and Atom→RSS bridges emit) competes for the same field. With
	// a scalar field the last element wins, so element order decides
	// whether the real link survives — and a lost link drops the whole item.
	rssMk := func(inner string) string {
		return `<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:media="http://search.yahoo.com/mrss/">` +
			`<channel><item>` + inner + `</item></channel></rss>`
	}
	atomMk := func(inner string) string {
		return `<feed xmlns="http://www.w3.org/2005/Atom" xmlns:media="http://search.yahoo.com/mrss/"` +
			` xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd">` +
			`<entry>` + inner + `</entry></feed>`
	}
	const (
		rssReal     = `<title>Real title</title><link>https://real.example/post</link><description>Real description</description>`
		rssForeign  = `<atom:link rel="self" href="https://feed.example/rss.xml"/><dc:title/><media:description/>`
		atomReal    = `<title>Real title</title><link rel="alternate" href="https://real.example/post"/><summary>Real description</summary>`
		atomForeign = `<media:title/><itunes:summary/>`
	)
	cases := []struct {
		name, doc string
	}{
		{"rss, real elements first", rssMk(rssReal + rssForeign)},
		{"rss, foreign elements first", rssMk(rssForeign + rssReal)},
		{"atom, real elements first", atomMk(atomReal + atomForeign)},
		{"atom, foreign elements first", atomMk(atomForeign + atomReal)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items, err := parseFeed([]byte(tc.doc))
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != 1 {
				t.Fatalf("items = %d, want 1 (a namespaced sibling must not blank a required field and drop the item)", len(items))
			}
			if items[0].Title != "Real title" {
				t.Errorf("Title = %q, want %q", items[0].Title, "Real title")
			}
			if items[0].URL != "https://real.example/post" {
				t.Errorf("URL = %q, want the item's own link, not the feed's self link", items[0].URL)
			}
			if items[0].Summary != "Real description" {
				t.Errorf("Summary = %q, want %q", items[0].Summary, "Real description")
			}
		})
	}
}

func TestParseFeed_RSSCategoriesTrimmedAndEmptiesDropped(t *testing.T) {
	// Tags reach Story.Tags, --style=json, and the diversity penalty, where
	// two stories both carrying "" would count as sharing a tag. The Atom
	// path already drops empty terms; RSS must match it.
	feed := "<rss version=\"2.0\"><channel><item><title>t</title><link>https://e.com/x</link>" +
		"<category></category><category>\n   zig\n</category><category>   </category>" +
		"<category>compilers</category></item></channel></rss>"
	items, err := parseFeed([]byte(feed))
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
	want := []string{"zig", "compilers"}
	got := items[0].Tags
	if len(got) != len(want) {
		t.Fatalf("Tags = %q, want %q (empties dropped, values trimmed)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Tags[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseFeed_AtomContentTypes(t *testing.T) {
	// <content> carries its text three ways. Only the xhtml form puts the
	// text in child elements, where a chardata-only decode sees nothing.
	mk := func(content string) string {
		return `<feed xmlns="http://www.w3.org/2005/Atom"><entry><title>E</title>` +
			`<link rel="alternate" href="https://e.org/1"/>` + content + `</entry></feed>`
	}
	cases := []struct {
		name, content, want string
	}{
		{"plain text", `<content>Hello world</content>`, "Hello world"},
		{"escaped html", `<content type="html">&lt;p&gt;Hello &amp; world&lt;/p&gt;</content>`, "Hello & world"},
		{"cdata", `<content><![CDATA[Hello <b>world</b>]]></content>`, "Hello world"},
		{"xhtml", `<content type="xhtml"><div><p>Hello <b>world</b></p></div></content>`, "Hello world"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items, err := parseFeed([]byte(mk(tc.content)))
			if err != nil || len(items) != 1 {
				t.Fatalf("items=%d err=%v", len(items), err)
			}
			if items[0].Summary != tc.want {
				t.Errorf("Summary = %q, want %q", items[0].Summary, tc.want)
			}
		})
	}
}
