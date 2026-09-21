package seed_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Quad4-Software/rss-discovery/internal/seed"
	"github.com/Quad4-Software/rss-discovery/internal/store"
)

func TestLoadHNBlogsSeed(t *testing.T) {
	src := filepath.Join("..", "..", "data", "seeds", "hn-blogs.jsonl")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hn-blogs.jsonl"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "t.duckdb"), 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	n, err := seed.LoadDir(context.Background(), st, dir)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1000 {
		t.Fatalf("expected large HN blogs seed, got %d", n)
	}
	var count int64
	if err := st.DB().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM feeds WHERE source=?`, "hn-blogs.jsonl").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count < 1000 {
		t.Fatalf("hn-blogs rows %d", count)
	}
}
