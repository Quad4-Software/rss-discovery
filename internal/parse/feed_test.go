package parse_test

import (
	"testing"

	"github.com/Quad4-Software/rss-discovery/internal/parse"
)

func TestParseRSS(t *testing.T) {
	body := []byte(`<?xml version="1.0"?>
<rss version="2.0"><channel>
<title>Demo</title><link>https://example.com</link>
<item><title>Hello</title><link>https://example.com/1</link><guid>g1</guid><description>World</description></item>
</channel></rss>`)
	res, err := parse.Feed(body, "feed1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != "RSS" {
		t.Fatalf("format %s", res.Format)
	}
	if len(res.Entries) != 1 || res.Entries[0].Title != "Hello" {
		t.Fatalf("%+v", res.Entries)
	}
}

func TestParseAtom(t *testing.T) {
	body := []byte(`<?xml version="1.0"?>
<feed xmlns="http://www.w3.org/2005/Atom">
<title>Atom Demo</title>
<entry><title>E1</title><id>id1</id><link href="https://example.com/e1"/></entry>
</feed>`)
	res, err := parse.Feed(body, "feed2")
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != "Atom" {
		t.Fatalf("format %s", res.Format)
	}
}

func TestParseJSONFeed(t *testing.T) {
	body := []byte(`{"version":"https://jsonfeed.org/version/1","title":"JF","items":[{"id":"1","url":"https://example.com/j","title":"J1"}]}`)
	res, err := parse.Feed(body, "feed3")
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != "JSON" {
		t.Fatalf("format %s", res.Format)
	}
	if len(res.Entries) != 1 {
		t.Fatalf("entries %d", len(res.Entries))
	}
}
