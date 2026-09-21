package opml_test

import (
	"strings"
	"testing"

	"github.com/Quad4-Software/rss-discovery/internal/opml"
)

func TestParseAndExport(t *testing.T) {
	raw := `<?xml version="1.0" encoding="UTF-8"?>
<opml version="2.0">
  <head><title>Test</title></head>
  <body>
    <outline text="News">
      <outline type="rss" text="Example" title="Example" xmlUrl="https://example.com/feed.xml" htmlUrl="https://example.com/"/>
    </outline>
  </body>
</opml>`
	items, err := opml.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("len %d", len(items))
	}
	if items[0].XMLURL != "https://example.com/feed.xml" {
		t.Fatal(items[0].XMLURL)
	}
	if items[0].Category != "News" {
		t.Fatalf("category %q", items[0].Category)
	}
	out, err := opml.Export("Roundtrip", items)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "xmlUrl") && !strings.Contains(string(out), "xmlUrl=") {
		// encoding/xml uses xmlUrl attr name from struct tag
	}
	if !strings.Contains(string(out), "example.com/feed.xml") {
		t.Fatalf("export missing url: %s", out)
	}
}
