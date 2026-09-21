package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Quad4-Software/rss-discovery/internal/auth"
	"github.com/Quad4-Software/rss-discovery/internal/config"
)

func TestAPISecuritySSRFMatrix(t *testing.T) {
	srv, st, _ := testServer(t, true)
	tok := mint(t, st, auth.LevelStandard)
	payloads := []string{
		`{"url":"http://127.0.0.1/feed.xml"}`,
		`{"url":"http://localhost/feed.xml"}`,
		`{"url":"http://[::1]/feed.xml"}`,
		`{"url":"http://10.0.0.5/feed.xml"}`,
		`{"url":"http://192.168.1.10/feed.xml"}`,
		`{"url":"http://169.254.169.254/latest/meta-data/"}`,
		`{"url":"file:///etc/passwd"}`,
		`{"url":"javascript:alert(1)"}`,
		`{"url":"ftp://example.com/x"}`,
		`{"url":"https://user:pass@example.com/feed.xml"}`,
		`{"url":"http://example.local/feed.xml"}`,
		`{"url":"http://svc.internal/feed.xml"}`,
	}
	for _, body := range payloads {
		w := doReq(t, srv, "POST", "/v1/discover", tok, "Quad4Test/1.0", []byte(body))
		if w.Code == 202 {
			t.Fatalf("SSRF payload accepted: %s -> %d %s", body, w.Code, w.Body.String())
		}
		if w.Code != 400 {
			t.Fatalf("want 400 for %s got %d %s", body, w.Code, w.Body.String())
		}
	}
}

func TestAPISecurityAuthBypass(t *testing.T) {
	srv, st, _ := testServer(t, true)
	admin := mint(t, st, auth.LevelAdmin)
	readonly := mint(t, st, auth.LevelReadonly)

	cases := []struct {
		method, path, token string
		body                []byte
		want                int
	}{
		{"GET", "/v1/feeds", "", nil, 401},
		{"GET", "/v1/admin/tokens", readonly, nil, 403},
		{"POST", "/v1/purge", readonly, []byte(`{}`), 403},
		{"POST", "/v1/admin/maintenance", readonly, []byte(`{"enabled":true}`), 403},
		{"DELETE", "/v1/admin/tokens/x", readonly, nil, 403},
		{"POST", "/v1/discover", readonly, []byte(`{"url":"https://example.com/a.xml"}`), 403},
		{"GET", "/v1/admin/tokens", admin, nil, 200},
		{"GET", "/v1/feeds", "rd_admin_notreal", nil, 401},
		{"GET", "/v1/feeds", "Bearer missing", nil, 401},
	}
	for _, tc := range cases {
		var w *httptest.ResponseRecorder
		if tc.token == "Bearer missing" {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.Header.Set("User-Agent", "Quad4Test/1.0")
			r.Header.Set("Authorization", "Bearer ")
			w = httptest.NewRecorder()
			srv.Echo().ServeHTTP(w, r)
		} else {
			w = doReq(t, srv, tc.method, tc.path, tc.token, "Quad4Test/1.0", tc.body)
		}
		if w.Code != tc.want {
			t.Fatalf("%s %s token=%q want %d got %d %s", tc.method, tc.path, tc.token, tc.want, w.Code, w.Body.String())
		}
	}
}

func TestAPISecurityHeaderAndPathAbuse(t *testing.T) {
	srv, st, _ := testServer(t, true)
	tok := mint(t, st, auth.LevelReadonly)

	r := httptest.NewRequest("GET", "/v1/feeds", nil)
	r.Header.Set("User-Agent", "Quad4Test/1.0")
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("X-Forwarded-For", "127.0.0.1, 8.8.8.8")
	r.Header.Set("X-Real-IP", "10.0.0.1")
	w := httptest.NewRecorder()
	srv.Echo().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("spoofed proxy headers should not break authed GET: %d", w.Code)
	}

	for _, path := range []string{
		"/v1/feeds/" + strings.Repeat("a", 3000),
		"/v1/feeds/%2e%2e/%2e%2e/etc/passwd",
		"/v1/../.env",
		"/.git/HEAD",
		"/actuator/env",
		"/server-status",
	} {
		w := doReq(t, srv, "GET", path, tok, "Quad4Test/1.0", nil)
		if w.Code == 200 && strings.Contains(path, ".env") {
			t.Fatalf("probe path served: %s", path)
		}
		if w.Code == 414 && !strings.Contains(path, strings.Repeat("a", 100)) {
			t.Fatalf("unexpected 414 for %s", path)
		}
	}
}

func TestAPISecurityBulkDiscoverFilters(t *testing.T) {
	srv, st, _ := testServer(t, true)
	tok := mint(t, st, auth.LevelStandard)
	body, _ := json.Marshal(map[string]any{
		"urls": []string{
			"https://example.com/ok.xml",
			"http://127.0.0.1/bad.xml",
			"javascript:alert(1)",
			"https://user:pass@example.com/x",
		},
	})
	w := doReq(t, srv, "POST", "/v1/discover/bulk", tok, "Quad4Test/1.0", body)
	if w.Code != 202 {
		t.Fatalf("bulk %d %s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("127.0.0.1")) {
		t.Fatal("response should not echo rejected SSRF targets as accepted")
	}
}

func TestAPISecurityPublicModeStillGuardsWrites(t *testing.T) {
	srv, _, _ := testServerWith(t, false, func(cfg *config.Config) {
		cfg.Auth.PublicBurst = 100
		cfg.Auth.PublicRatePerMin = 600
	})
	for _, path := range []string{"/v1/purge", "/v1/admin/tokens", "/v1/webhooks"} {
		method := "GET"
		var body []byte
		if path == "/v1/purge" || path == "/v1/webhooks" {
			method = "POST"
			body = []byte(`{}`)
			if path == "/v1/webhooks" {
				body = []byte(`{"url":"https://example.com/hook","events":"feed.updated"}`)
			}
		}
		w := doReq(t, srv, method, path, "", "Quad4Test/1.0", body)
		if w.Code == 200 || w.Code == 201 || w.Code == 202 {
			t.Fatalf("anonymous must not access %s %s got %d", method, path, w.Code)
		}
	}
}

func FuzzAPIPaths(f *testing.F) {
	for _, p := range []string{"/v1/feeds", "/healthz", "/.env", "/wp-admin", "/openapi.json", "/v1/admin/tokens", "/"} {
		f.Add(p, "GET")
		f.Add(p, "POST")
	}
	srv, _, _ := testServer(f, true)
	f.Fuzz(func(t *testing.T, path, method string) {
		if len(path) > 2048 || len(method) > 16 {
			return
		}
		switch method {
		case "GET", "POST", "HEAD", "DELETE":
		default:
			return
		}
		if path == "" || !strings.HasPrefix(path, "/") {
			return
		}
		if strings.ContainsAny(path, " \t\r\n\x00") {
			return
		}
		r, err := http.NewRequest(method, "http://example.com"+path, nil)
		if err != nil {
			return
		}
		r.Header.Set("User-Agent", "Quad4Test/1.0")
		w := httptest.NewRecorder()
		srv.Echo().ServeHTTP(w, r)
		if w.Code >= 500 && w.Code != 503 {
			t.Fatalf("server error for %s %s: %d %s", method, path, w.Code, w.Body.String())
		}
	})
}
