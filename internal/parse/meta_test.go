package parse_test

import (
	"testing"
	"testing/quick"

	"github.com/Quad4-Software/rss-discovery/internal/parse"
	"github.com/Quad4-Software/rss-discovery/internal/store"
)

func TestMetamorphicParseRoundTripIdentity(t *testing.T) {
	// Same body parsed twice yields same entry IDs (content-addressed stability).
	body := []byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>T</title>
<item><title>A</title><guid>g1</guid><link>https://ex.com/a</link></item></channel></rss>`)
	a, err := parse.Feed(body, "fid")
	if err != nil {
		t.Fatal(err)
	}
	b, err := parse.Feed(body, "fid")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Entries) != len(b.Entries) || a.Entries[0].ID != b.Entries[0].ID {
		t.Fatalf("%v vs %v", a.Entries, b.Entries)
	}
	if a.Entries[0].ID != store.EntryID("fid", "g1", "https://ex.com/a") {
		t.Fatal("id formula")
	}
}

func TestPropertyFeedIDStable(t *testing.T) {
	f := func(s string) bool {
		if s == "" {
			return true
		}
		return store.FeedID(s) == store.FeedID(s)
	}
	if err := quick.Check(f, nil); err != nil {
		t.Fatal(err)
	}
}
