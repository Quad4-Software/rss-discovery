package api

import (
	"embed"
	"net/http"
	"regexp"
	"strings"

	"github.com/labstack/echo/v4"
)

//go:embed openapi.json
var openAPISpec []byte

//go:embed docs
var docsAssets embed.FS

var docsAssetName = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)

func (s *Server) openapiJSON(c echo.Context) error {
	return c.Blob(http.StatusOK, "application/json; charset=utf-8", openAPISpec)
}

func (s *Server) docsHTML(c echo.Context) error {
	return c.HTMLBlob(http.StatusOK, docsHTMLPage)
}

// docsAsset serves the vendored Scalar bundle and fonts from the embedded
// docs directory. Paths are whitelisted against the embedded fs so nothing
// outside it can be reached.
func (s *Server) docsAsset(c echo.Context) error {
	name := c.Param("*")
	if !docsAssetName.MatchString(name) || strings.Contains(name, "..") {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	data, err := docsAssets.ReadFile("docs/" + name)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	ctype := "application/octet-stream"
	switch {
	case strings.HasSuffix(name, ".js"):
		ctype = "text/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".woff2"):
		ctype = "font/woff2"
	}
	// assets are version pinned at build time so they can cache forever
	c.Response().Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	return c.Blob(http.StatusOK, ctype, data)
}

// Scalar standalone build, fully self-hosted under /docs/assets. See
// docs/VENDORED.md for version and provenance.
var docsHTMLPage = []byte(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<meta name="color-scheme" content="dark"/>
<title>RSS Discovery API</title>
<style>html,body{margin:0;background:#0f1419}</style>
</head>
<body>
<div id="app"></div>
<noscript><p style="color:#e7ecf1;font-family:sans-serif;padding:2rem">
API docs need JavaScript. The raw spec is at
<a style="color:#5b9fd4" href="/openapi.json">/openapi.json</a>.</p></noscript>
<script src="/docs/assets/scalar-1.68.0.js"></script>
<script>
Scalar.createApiReference('#app', {
  url: '/openapi.json',
  theme: 'deepSpace',
  forceDarkModeState: 'dark',
  hideDarkModeToggle: true,
})
</script>
</body>
</html>`)
