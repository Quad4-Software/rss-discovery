package parse_test

import (
	"testing"

	"github.com/Quad4-Software/rss-discovery/internal/parse"
)

var sampleRSS = []byte(`<?xml version="1.0"?>
<rss version="2.0"><channel>
<title>Bench Feed</title>
<link>https://example.com/</link>
<item><title>One</title><link>https://example.com/1</link><guid>1</guid><description>Hello</description></item>
<item><title>Two</title><link>https://example.com/2</link><guid>2</guid><description>World</description></item>
</channel></rss>`)

func BenchmarkFeedParse(b *testing.B) {
	b.ReportAllocs()
	b.SetBytes(int64(len(sampleRSS)))
	for i := 0; i < b.N; i++ {
		res, err := parse.Feed(sampleRSS, "feed-bench")
		if err != nil {
			b.Fatal(err)
		}
		if len(res.Entries) != 2 {
			b.Fatal(len(res.Entries))
		}
	}
}
