package auth_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Quad4-Software/rss-discovery/internal/auth"
)

func FuzzParseLevel(f *testing.F) {
	for _, s := range []string{"readonly", "standard", "priority", "admin", "Admin", "", "nope", "root", "READONLY"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !utf8.ValidString(s) {
			return
		}
		lv, err := auth.ParseLevel(s)
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "readonly", "standard", "priority", "admin":
			if err != nil || lv.Rank() < 1 {
				t.Fatalf("valid level %q -> %v %v", s, lv, err)
			}
		default:
			if err == nil {
				t.Fatalf("invalid level accepted: %q -> %v", s, lv)
			}
		}
	})
}

func FuzzHashToken(f *testing.F) {
	for _, s := range []string{"", "rd_admin_abc", "a", strings.Repeat("x", 4096)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !utf8.ValidString(s) {
			return
		}
		h1 := auth.HashToken(s)
		h2 := auth.HashToken(s)
		if h1 != h2 {
			t.Fatal("hash not stable")
		}
		if len(h1) != 64 {
			t.Fatalf("unexpected digest len %d", len(h1))
		}
		if s != "" && auth.EqualHash(h1, auth.HashToken(s+"x")) {
			t.Fatal("collision with suffix")
		}
	})
}

func FuzzAllows(f *testing.F) {
	levels := []string{"readonly", "standard", "priority", "admin"}
	methods := []string{"GET", "POST", "DELETE", "PUT", "HEAD", "PATCH", "OPTIONS"}
	paths := []string{
		"/v1/feeds", "/v1/admin/tokens", "/v1/purge", "/v1/webhooks",
		"/healthz", "/openapi.json", "/v1/discover", "/evil", "",
	}
	for _, lv := range levels {
		for _, m := range methods {
			for _, p := range paths {
				f.Add(lv, m, p)
			}
		}
	}
	f.Fuzz(func(t *testing.T, level, method, path string) {
		if !utf8.ValidString(level) || !utf8.ValidString(method) || !utf8.ValidString(path) {
			return
		}
		lv, err := auth.ParseLevel(level)
		if err != nil {
			return
		}
		ok := auth.Allows(lv, method, path)
		if strings.HasPrefix(path, "/v1/admin/") && lv != auth.LevelAdmin && ok {
			t.Fatalf("non-admin allowed on admin path: %s %s %s", level, method, path)
		}
		if path == "/v1/purge" && lv != auth.LevelAdmin && ok {
			t.Fatalf("non-admin allowed purge: %s", level)
		}
		if method == "POST" && path == "/v1/feeds" && lv == auth.LevelReadonly && ok {
			t.Fatal("readonly must not POST feeds")
		}
	})
}
