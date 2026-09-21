package api

import (
	_ "embed"
	"net/http"

	"github.com/labstack/echo/v4"
)

//go:embed openapi.json
var openAPISpec []byte

func (s *Server) openapiJSON(c echo.Context) error {
	return c.Blob(http.StatusOK, "application/json; charset=utf-8", openAPISpec)
}

func (s *Server) docsHTML(c echo.Context) error {
	return c.HTMLBlob(http.StatusOK, docsHTMLPage)
}

// Minimal self-hosted docs (no CDN). Full machine-readable contract is /openapi.json.
var docsHTMLPage = []byte(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>RSS Discovery API</title>
<style>
:root { --bg:#0f1419; --fg:#e7ecf1; --muted:#9aa7b5; --accent:#5b9fd4; --line:#243040; }
html,body { margin:0; background:var(--bg); color:var(--fg); font:16px/1.5 ui-sans-serif, system-ui, sans-serif; }
main { max-width: 52rem; margin: 0 auto; padding: 2.5rem 1.25rem 4rem; }
h1 { font-size: 1.75rem; letter-spacing: -0.02em; margin: 0 0 .5rem; }
p { color: var(--muted); margin: 0 0 1.25rem; }
a { color: var(--accent); }
code, pre { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
pre { background: #16202a; border: 1px solid var(--line); padding: 1rem; overflow: auto; border-radius: 6px; }
ul { padding-left: 1.2rem; color: var(--muted); }
li { margin: .35rem 0; }
.badge { display:inline-block; font-size:.75rem; border:1px solid var(--line); padding:.15rem .45rem; border-radius:4px; color:var(--fg); margin-right:.35rem; }
</style>
</head>
<body>
<main>
  <h1>RSS Discovery API</h1>
  <p>Public feed catalog, search, discovery, and full-text cache. Authenticate with <code>Authorization: Bearer rd_&lt;level&gt;_&lt;secret&gt;</code> or <code>X-API-Key</code>. When <code>auth.required=false</code>, anonymous clients may call readonly GETs under a strict public rate limit while token clients keep higher limits and reserved capacity.</p>
  <p><a href="/openapi.json">OpenAPI 3.1 JSON</a> &middot; Levels: readonly, standard, priority, admin</p>
  <h2>Core</h2>
  <ul>
    <li><span class="badge">GET</span> <code>/healthz</code> <code>/livez</code> <code>/readyz</code></li>
    <li><span class="badge">GET</span> <code>/v1/stats</code> (ETag / CDN cache)</li>
    <li><span class="badge">GET</span> <code>/v1/feeds</code> list &middot; <span class="badge">POST</span> add</li>
    <li><span class="badge">GET</span> <code>/v1/feeds/{id}</code> &middot; status &middot; health &middot; refresh</li>
    <li><span class="badge">GET</span> <code>/v1/search/feeds|entries|content?q=</code></li>
    <li><span class="badge">GET</span> <code>/v1/similar/feeds/{id}?method=jaccard|embedding</code></li>
    <li><span class="badge">GET/POST</span> <code>/v1/opml</code> &middot; <span class="badge">POST</span> <code>/v1/discover/bulk</code> &middot; <code>/v1/jobs</code></li>
    <li><span class="badge">GET</span> <code>/v1/status/freshness</code> &middot; <code>/v1/health/feeds</code></li>
    <li><span class="badge">POST</span> <code>/oauth/token</code> (client_credentials)</li>
  </ul>
  <h2>Admin</h2>
  <ul>
    <li><span class="badge">POST</span> <code>/v1/purge</code></li>
    <li><span class="badge">GET/POST</span> <code>/v1/admin/maintenance</code></li>
    <li><span class="badge">GET/POST/DELETE</span> <code>/v1/admin/tokens</code> &middot; oauth clients</li>
    <li><span class="badge">GET/POST/DELETE</span> <code>/v1/webhooks</code> &middot; <code>/v1/websub</code></li>
    <li><span class="badge">GET</span> <code>/v1/admin/access-logs</code></li>
  </ul>
  <h2>Limits</h2>
  <p>Rate limits apply per IP for anonymous traffic and per token for authenticated clients. Under load, public requests are shed first so authed clients keep reserved concurrency. Readonly GETs advertise <code>Cache-Control: public, max-age, stale-while-revalidate</code> and weak ETags. WebSub callbacks and OAuth token endpoint are public.</p>
  <pre>curl -sH "Authorization: Bearer $TOKEN" -H "User-Agent: MyApp/1.0" \
  "https://example/v1/search/feeds?q=golang"</pre>
</main>
</body>
</html>`)
