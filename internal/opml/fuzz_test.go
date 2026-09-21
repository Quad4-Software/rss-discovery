package opml_test

import (
	"testing"
	"unicode/utf8"

	"github.com/Quad4-Software/rss-discovery/internal/opml"
)

func FuzzParse(f *testing.F) {
	f.Add([]byte(`<?xml version="1.0"?><opml version="2.0"><head><title>t</title></head><body><outline type="rss" text="a" xmlUrl="https://example.com/feed.xml"/></body></opml>`))
	f.Add([]byte(`not xml`))
	f.Add([]byte(``))
	f.Add([]byte(`<?xml version="1.0"?><opml version="2.0"><body><outline xmlUrl="javascript:alert(1)"/></body></opml>`))
	f.Add([]byte(`<?xml version="1.0"?><opml version="2.0"><body>` + string(make([]byte, 1024)) + `</body></opml>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		if !utf8.Valid(data) {
			return
		}
		items, err := opml.Parse(data)
		if err != nil {
			return
		}
		for _, it := range items {
			if it.XMLURL == "" {
				t.Fatal("empty xml url in parsed outline")
			}
		}
	})
}
