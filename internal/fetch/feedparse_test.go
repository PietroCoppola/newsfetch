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
