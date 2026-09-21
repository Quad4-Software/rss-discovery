package auth_test

import (
	"testing"
	"time"

	"github.com/Quad4-Software/rss-discovery/internal/auth"
)

func TestGenerateAndHash(t *testing.T) {
	plain, rec, err := auth.Generate(auth.LevelPriority, "t", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Level != auth.LevelPriority || rec.Name != "t" || rec.ExpiresAt == nil {
		t.Fatalf("%+v", rec)
	}
	if auth.HashToken(plain) != rec.Hash {
		t.Fatal("hash mismatch")
	}
	if !auth.EqualHash(rec.Hash, auth.HashToken(plain)) {
		t.Fatal("equal hash")
	}
	if auth.EqualHash(rec.Hash, auth.HashToken(plain+"x")) {
		t.Fatal("should not equal")
	}
}

func TestAllows(t *testing.T) {
	if !auth.Allows(auth.LevelReadonly, "GET", "/v1/feeds") {
		t.Fatal("readonly get")
	}
	if auth.Allows(auth.LevelReadonly, "POST", "/v1/feeds") {
		t.Fatal("readonly post denied")
	}
	if !auth.Allows(auth.LevelStandard, "POST", "/v1/discover") {
		t.Fatal("standard discover")
	}
	if auth.Allows(auth.LevelStandard, "POST", "/v1/purge") {
		t.Fatal("purge admin only")
	}
	if !auth.Allows(auth.LevelAdmin, "POST", "/v1/purge") {
		t.Fatal("admin purge")
	}
}

func TestAnonymousRateLimitDefaults(t *testing.T) {
	perMin, burst := auth.AnonymousRateLimit(0, 0)
	if perMin != 60 || burst != 10 {
		t.Fatalf("defaults %d/%d", perMin, burst)
	}
	perMin, burst = auth.AnonymousRateLimit(120, 20)
	if perMin != 120 || burst != 20 {
		t.Fatalf("custom %d/%d", perMin, burst)
	}
}

