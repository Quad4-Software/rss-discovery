package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Quad4-Software/rss-discovery/internal/auth"
	"github.com/Quad4-Software/rss-discovery/internal/store"
)

func TestTokenLifecycle(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.duckdb"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	plain, rec, err := auth.Generate(auth.LevelAdmin, "ops", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateToken(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := st.LookupTokenByHash(ctx, auth.HashToken(plain))
	if err != nil || got == nil || got.ID != rec.ID {
		t.Fatalf("%v %+v", err, got)
	}
	if err := store.ActiveToken(got); err != nil {
		t.Fatal(err)
	}
	ok, err := st.RevokeToken(ctx, rec.ID)
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	got, _ = st.LookupTokenByHash(ctx, auth.HashToken(plain))
	if store.ActiveToken(got) == nil {
		t.Fatal("should be revoked")
	}
	_ = st.InsertAccessLog(ctx, auth.AccessLog{
		TokenID: rec.ID, Level: string(rec.Level), Method: "GET", Path: "/v1/feeds",
		Status: 200, IP: "127.0.0.1", UA: "t", LatencyMs: 1, CreatedAt: time.Now().UTC(),
	})
	logs, err := st.ListAccessLogs(ctx, rec.ID, 10)
	if err != nil || len(logs) < 1 {
		t.Fatalf("%v %d", err, len(logs))
	}
}
