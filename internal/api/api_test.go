package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Quad4-Software/rss-discovery/internal/api"
	"github.com/Quad4-Software/rss-discovery/internal/auth"
	"github.com/Quad4-Software/rss-discovery/internal/blob"
	"github.com/Quad4-Software/rss-discovery/internal/config"
	"github.com/Quad4-Software/rss-discovery/internal/fetch"
	"github.com/Quad4-Software/rss-discovery/internal/observ"
	"github.com/Quad4-Software/rss-discovery/internal/security"
	"github.com/Quad4-Software/rss-discovery/internal/store"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreCurrent(),
	)
}

func testServer(t testing.TB, authRequired bool) (*api.Server, *store.Store, string) {
	return testServerWith(t, authRequired, nil)
}

func testServerWith(t testing.TB, authRequired bool, mutate func(*config.Config)) (*api.Server, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.DBPath = filepath.Join(dir, "t.duckdb")
	cfg.SeedDir = dir
	cfg.Landlock = false
	cfg.Auth.Required = authRequired
	cfg.Auth.AllowAnonymous = true
	cfg.Security.RequireUA = true
	cfg.Security.BlockProbes = true
	cfg.Security.BlockScanners = true
	cfg.Security.BlockScrapers = true
	cfg.Observ.AccessLog = false
	cfg.Observ.PrivacyMode = true
	cfg.Metrics.Addr = ""
	cfg.Server.Addr = "127.0.0.1:0"
	if mutate != nil {
		mutate(&cfg)
	}

	st, err := store.Open(cfg.DBPath, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	blobs, err := blob.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	pool := fetch.NewPool(cfg, st, blobs)
	pool.Start(context.Background())
	t.Cleanup(pool.Stop)
	obs, err := observ.Setup(observ.Options{ServiceName: "test", LogLevel: "error", PrivacyMode: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { obs.Close(context.Background()) })
	srv := api.New(cfg, st, pool, obs)
	return srv, st, dir
}

func mint(t testing.TB, st *store.Store, level auth.Level) string {
	t.Helper()
	plain, rec, err := auth.Generate(level, "test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateToken(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	return plain
}

func doReq(t testing.TB, srv *api.Server, method, path, token, ua string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if ua != "-" {
		if ua == "" {
			ua = "Quad4Test/1.0"
		}
		r.Header.Set("User-Agent", ua)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	srv.Echo().ServeHTTP(w, r)
	return w
}

func TestSmokeHealthAndAuth(t *testing.T) {
	srv, st, _ := testServer(t, true)
	w := doReq(t, srv, "GET", "/healthz", "", "Quad4Test/1.0", nil)
	if w.Code != 200 {
		t.Fatalf("health %d", w.Code)
	}
	w = doReq(t, srv, "GET", "/v1/feeds", "", "Quad4Test/1.0", nil)
	if w.Code != 401 {
		t.Fatalf("want 401 got %d", w.Code)
	}
	tok := mint(t, st, auth.LevelReadonly)
	w = doReq(t, srv, "GET", "/v1/feeds", tok, "Quad4Test/1.0", nil)
	if w.Code != 200 {
		t.Fatalf("feeds %d %s", w.Code, w.Body.String())
	}
	w = doReq(t, srv, "POST", "/v1/purge", tok, "Quad4Test/1.0", []byte(`{}`))
	if w.Code != 403 {
		t.Fatalf("readonly purge %d", w.Code)
	}
	admin := mint(t, st, auth.LevelAdmin)
	w = doReq(t, srv, "POST", "/v1/purge", admin, "Quad4Test/1.0", []byte(`{}`))
	if w.Code != 200 {
		t.Fatalf("admin purge %d %s", w.Code, w.Body.String())
	}
}

func TestPublicModeReadonlyAndWrites(t *testing.T) {
	srv, st, _ := testServer(t, false)
	w := doReq(t, srv, "GET", "/v1/feeds", "", "Quad4Test/1.0", nil)
	if w.Code != 200 {
		t.Fatalf("public feeds %d %s", w.Code, w.Body.String())
	}
	w = doReq(t, srv, "GET", "/v1/stats", "", "Quad4Test/1.0", nil)
	if w.Code != 200 {
		t.Fatalf("public stats %d", w.Code)
	}
	w = doReq(t, srv, "POST", "/v1/discover", "", "Quad4Test/1.0", []byte(`{"url":"https://example.com/feed.xml"}`))
	if w.Code != 401 {
		t.Fatalf("anonymous write want 401 got %d", w.Code)
	}
	w = doReq(t, srv, "POST", "/v1/purge", "", "Quad4Test/1.0", []byte(`{}`))
	if w.Code != 401 {
		t.Fatalf("anonymous purge want 401 got %d", w.Code)
	}
	tok := mint(t, st, auth.LevelStandard)
	w = doReq(t, srv, "POST", "/v1/discover", tok, "Quad4Test/1.0", []byte(`{"url":"https://example.com/feed.xml"}`))
	if w.Code != 202 {
		t.Fatalf("authed discover %d %s", w.Code, w.Body.String())
	}
}

func TestPublicModeRateLimit(t *testing.T) {
	srv, _, _ := testServerWith(t, false, func(cfg *config.Config) {
		cfg.Auth.PublicRatePerMin = 30
		cfg.Auth.PublicBurst = 2
	})
	hit429 := false
	for i := 0; i < 20; i++ {
		w := doReq(t, srv, "GET", "/v1/feeds", "", "Quad4Test/1.0", nil)
		if w.Code == 429 {
			hit429 = true
			if w.Header().Get("X-RateLimit-Scope") != "public" {
				t.Fatalf("want public scope, got %q", w.Header().Get("X-RateLimit-Scope"))
			}
			break
		}
		if w.Code != 200 {
			t.Fatalf("unexpected %d", w.Code)
		}
	}
	if !hit429 {
		t.Fatal("expected public rate limit 429")
	}
}

func TestAdversarialProbesAndScanners(t *testing.T) {
	srv, _, _ := testServer(t, false)
	cases := []struct {
		path string
		ua   string
		code int
	}{
		{"/.env", "Mozilla/5.0", 404},
		{"/wp-admin", "Mozilla/5.0", 404},
		{"/v1/feeds", "sqlmap/1.5", 403},
		{"/v1/feeds", "Bytespider", 403},
		{"/v1/feeds", "-", 400},
	}
	for _, tc := range cases {
		w := doReq(t, srv, "GET", tc.path, "", tc.ua, nil)
		if w.Code != tc.code {
			t.Fatalf("%s ua=%q want %d got %d", tc.path, tc.ua, tc.code, w.Code)
		}
	}
}

func TestMaintenanceMode(t *testing.T) {
	srv, st, _ := testServer(t, true)
	tok := mint(t, st, auth.LevelStandard)
	admin := mint(t, st, auth.LevelAdmin)
	security.SetMaintenance(true)
	t.Cleanup(func() { security.SetMaintenance(false) })
	w := doReq(t, srv, "GET", "/v1/feeds", tok, "Quad4Test/1.0", nil)
	if w.Code != 503 {
		t.Fatalf("maint %d", w.Code)
	}
	w = doReq(t, srv, "GET", "/healthz", "", "Quad4Test/1.0", nil)
	if w.Code != 200 {
		t.Fatal("health during maint")
	}
	w = doReq(t, srv, "GET", "/v1/feeds", admin, "Quad4Test/1.0", nil)
	if w.Code != 200 {
		t.Fatalf("admin bypass %d", w.Code)
	}
	body, _ := json.Marshal(map[string]bool{"enabled": false})
	w = doReq(t, srv, "POST", "/v1/admin/maintenance", admin, "Quad4Test/1.0", body)
	if w.Code != 200 {
		t.Fatalf("disable %d", w.Code)
	}
}

func TestRevokedAndExpiredToken(t *testing.T) {
	srv, st, _ := testServer(t, true)
	plain, rec, err := auth.Generate(auth.LevelStandard, "x", -time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// force expired
	exp := time.Now().UTC().Add(-time.Hour)
	rec.ExpiresAt = &exp
	if err := st.CreateToken(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	w := doReq(t, srv, "GET", "/v1/feeds", plain, "Quad4Test/1.0", nil)
	if w.Code != 401 {
		t.Fatalf("expired %d", w.Code)
	}
	plain2, rec2, _ := auth.Generate(auth.LevelStandard, "y", time.Hour)
	_ = st.CreateToken(context.Background(), rec2)
	_, _ = st.RevokeToken(context.Background(), rec2.ID)
	w = doReq(t, srv, "GET", "/v1/feeds", plain2, "Quad4Test/1.0", nil)
	if w.Code != 401 {
		t.Fatalf("revoked %d", w.Code)
	}
}

func TestRaceAuthLookups(t *testing.T) {
	srv, st, _ := testServer(t, true)
	tok := mint(t, st, auth.LevelReadonly)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := doReq(t, srv, "GET", "/healthz", tok, "Quad4Test/1.0", nil)
			if w.Code != 200 {
				t.Errorf("code %d", w.Code)
			}
		}()
	}
	wg.Wait()
}

func TestMockDiscoverValidation(t *testing.T) {
	srv, st, _ := testServer(t, true)
	tok := mint(t, st, auth.LevelStandard)
	w := doReq(t, srv, "POST", "/v1/discover", tok, "Quad4Test/1.0", []byte(`{"url":"javascript:alert(1)"}`))
	if w.Code != 400 {
		t.Fatalf("bad scheme %d", w.Code)
	}
	w = doReq(t, srv, "POST", "/v1/discover", tok, "Quad4Test/1.0", []byte(`{"url":"http://127.0.0.1/feed.xml"}`))
	if w.Code != 400 {
		t.Fatalf("ssrf loopback %d", w.Code)
	}
	w = doReq(t, srv, "POST", "/v1/discover", tok, "Quad4Test/1.0", []byte(`{"url":"https://example.com/feed.xml"}`))
	if w.Code != 202 {
		t.Fatalf("discover %d %s", w.Code, w.Body.String())
	}
}

func TestOpenAPIAndDocs(t *testing.T) {
	srv, _, _ := testServer(t, true)
	w := doReq(t, srv, "GET", "/openapi.json", "", "Quad4Test/1.0", nil)
	if w.Code != 200 {
		t.Fatalf("openapi %d", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"openapi"`)) {
		t.Fatal("missing openapi key")
	}
	w = doReq(t, srv, "GET", "/docs", "", "Quad4Test/1.0", nil)
	if w.Code != 200 {
		t.Fatalf("docs %d", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("scalar")) {
		t.Fatal("docs page does not mount scalar")
	}
	w = doReq(t, srv, "GET", "/docs/assets/scalar-1.68.0.js", "", "Quad4Test/1.0", nil)
	if w.Code != 200 {
		t.Fatalf("docs asset %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("docs asset content type %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("docs asset cache control %q", cc)
	}
	w = doReq(t, srv, "GET", "/docs/assets/fonts/inter-latin.woff2", "", "Quad4Test/1.0", nil)
	if w.Code != 200 {
		t.Fatalf("docs font %d", w.Code)
	}
	w = doReq(t, srv, "GET", "/docs/assets/../openapi.json", "", "Quad4Test/1.0", nil)
	if w.Code == 200 {
		t.Fatal("path traversal served")
	}
}

func TestFloodAmbiguousFraming(t *testing.T) {
	srv, st, _ := testServer(t, true)
	tok := mint(t, st, auth.LevelReadonly)
	r := httptest.NewRequest("GET", "/v1/feeds", nil)
	r.Header.Set("User-Agent", "Quad4Test/1.0")
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("Transfer-Encoding", "chunked")
	r.Header.Set("Content-Length", "10")
	w := httptest.NewRecorder()
	srv.Echo().ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("want 400 got %d", w.Code)
	}
}

func TestFloodURITooLong(t *testing.T) {
	srv, _, _ := testServer(t, false)
	long := "/" + strings.Repeat("a", 3000)
	w := doReq(t, srv, "GET", long, "", "Quad4Test/1.0", nil)
	if w.Code != 414 {
		t.Fatalf("want 414 got %d", w.Code)
	}
}

func TestAPINoStoreHeader(t *testing.T) {
	srv, st, _ := testServer(t, true)
	tok := mint(t, st, auth.LevelReadonly)
	w := doReq(t, srv, "GET", "/v1/feeds", tok, "Quad4Test/1.0", nil)
	cc := w.Header().Get("Cache-Control")
	if cc != "private, no-store" {
		t.Fatalf("authed responses must be private, got %q", cc)
	}
}

func TestOPMLExportAndBulkJob(t *testing.T) {
	srv, st, _ := testServer(t, true)
	tok := mint(t, st, auth.LevelStandard)
	_ = st.UpsertFeed(context.Background(), store.Feed{ID: store.FeedID("https://example.com/a.xml"), URL: "https://example.com/a.xml", Title: "A"})
	w := doReq(t, srv, "GET", "/v1/opml", tok, "Quad4Test/1.0", nil)
	if w.Code != 200 {
		t.Fatalf("opml export %d", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("opml")) {
		t.Fatal("not opml")
	}
	body := []byte(`{"urls":["https://example.com/feed.xml"]}`)
	w = doReq(t, srv, "POST", "/v1/discover/bulk", tok, "Quad4Test/1.0", body)
	if w.Code != 202 {
		t.Fatalf("bulk %d %s", w.Code, w.Body.String())
	}
}

func TestFreshnessAndHealthEndpoints(t *testing.T) {
	srv, st, _ := testServer(t, true)
	tok := mint(t, st, auth.LevelReadonly)
	id := store.FeedID("https://example.com/h.xml")
	_ = st.UpsertFeed(context.Background(), store.Feed{ID: id, URL: "https://example.com/h.xml", Title: "H"})
	w := doReq(t, srv, "GET", "/v1/feeds/"+id+"/status", tok, "Quad4Test/1.0", nil)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	w = doReq(t, srv, "GET", "/v1/status/freshness", tok, "Quad4Test/1.0", nil)
	if w.Code != 200 {
		t.Fatalf("freshness %d", w.Code)
	}
	w = doReq(t, srv, "GET", "/v1/feeds/"+id+"/health", tok, "Quad4Test/1.0", nil)
	if w.Code != 200 {
		t.Fatalf("health %d", w.Code)
	}
}

func TestWebhookCRUD(t *testing.T) {
	srv, st, _ := testServer(t, true)
	tok := mint(t, st, auth.LevelStandard)
	body := []byte(`{"url":"https://example.com/hook","events":"feed.updated"}`)
	w := doReq(t, srv, "POST", "/v1/webhooks", tok, "Quad4Test/1.0", body)
	if w.Code != 201 {
		t.Fatalf("create webhook %d %s", w.Code, w.Body.String())
	}
	w = doReq(t, srv, "GET", "/v1/webhooks", tok, "Quad4Test/1.0", nil)
	if w.Code != 200 {
		t.Fatalf("list %d", w.Code)
	}
}

func TestOAuthClientCredentials(t *testing.T) {
	srv, st, _ := testServer(t, true)
	admin := mint(t, st, auth.LevelAdmin)
	body := []byte(`{"name":"mobile","level":"readonly"}`)
	w := doReq(t, srv, "POST", "/v1/admin/oauth/clients", admin, "Quad4Test/1.0", body)
	if w.Code != 201 {
		t.Fatalf("create client %d %s", w.Code, w.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	cid, _ := created["client_id"].(string)
	sec, _ := created["client_secret"].(string)
	form := "grant_type=client_credentials&client_id=" + cid + "&client_secret=" + sec
	r := httptest.NewRequest("POST", "/oauth/token", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("User-Agent", "Quad4Test/1.0")
	rec := httptest.NewRecorder()
	srv.Echo().ServeHTTP(rec, r)
	if rec.Code != 200 {
		t.Fatalf("token %d %s", rec.Code, rec.Body.String())
	}
}

func TestWebSubChallengeRequiresKnownTopic(t *testing.T) {
	srv, _, _ := testServer(t, false)
	r := httptest.NewRequest("GET", "/v1/websub/callback?hub.mode=subscribe&hub.topic=https://example.com/feed&hub.challenge=abc", nil)
	r.Header.Set("User-Agent", "WebSub/1.0")
	w := httptest.NewRecorder()
	srv.Echo().ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("unknown topic want 404 got %d", w.Code)
	}
}

func TestConditionalETag(t *testing.T) {
	srv, st, _ := testServer(t, true)
	tok := mint(t, st, auth.LevelReadonly)
	w := doReq(t, srv, "GET", "/v1/feeds", tok, "Quad4Test/1.0", nil)
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing etag")
	}
	r := httptest.NewRequest("GET", "/v1/feeds", nil)
	r.Header.Set("User-Agent", "Quad4Test/1.0")
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	srv.Echo().ServeHTTP(rec, r)
	if rec.Code != 304 {
		t.Fatalf("want 304 got %d", rec.Code)
	}
}

func TestMoreProbePaths(t *testing.T) {
	srv, _, _ := testServer(t, false)
	for _, p := range []string{"/.git/config", "/actuator/env", "/phpinfo.php", "/_ignition/health-check"} {
		w := doReq(t, srv, "GET", p, "", "Mozilla/5.0", nil)
		if w.Code != 404 {
			t.Fatalf("%s want 404 got %d", p, w.Code)
		}
	}
}
