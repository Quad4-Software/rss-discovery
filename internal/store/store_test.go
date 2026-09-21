package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Quad4-Software/rss-discovery/internal/store"
)

func TestStoreSeedSearchPurge(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "t.duckdb")
	st, err := store.Open(dbPath, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	feeds := []store.Feed{
		{URL: "https://example.com/a.xml", Title: "Alpha Security", Category: "Security", Source: "test"},
		{URL: "https://example.com/b.xml", Title: "Beta Space", Category: "Space", Source: "test"},
		{URL: "https://example.com/c.xml", Title: "Gamma Security Notes", Category: "Security", Source: "test"},
	}
	n, err := st.UpsertFeedsBatch(ctx, feeds)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("n=%d", n)
	}
	found, err := st.SearchFeeds(ctx, "Security", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) < 2 {
		t.Fatalf("search got %d", len(found))
	}
	fid := store.FeedID(feeds[0].URL)
	_ = st.ReplaceEntries(ctx, fid, []store.Entry{
		{GUID: "1", URL: "https://example.com/1", Title: "One", Summary: "hello world"},
	}, 50)
	stats, err := st.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats["feeds"].(int64) < 3 {
		t.Fatalf("stats %+v", stats)
	}
	_, err = st.Purge(ctx, 1<<30, 1<<20, 720*time.Hour, 1*time.Hour, 1*time.Hour, 100)
	if err != nil {
		t.Fatal(err)
	}
}
