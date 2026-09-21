package api

import (
	"context"
	"crypto/subtle"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/Quad4-Software/rss-discovery/internal/auth"
	"github.com/Quad4-Software/rss-discovery/internal/config"
	"github.com/Quad4-Software/rss-discovery/internal/fetch"
	"github.com/Quad4-Software/rss-discovery/internal/observ"
	"github.com/Quad4-Software/rss-discovery/internal/security"
	"github.com/Quad4-Software/rss-discovery/internal/store"
)

// Server is the Echo HTTP API.
type Server struct {
	cfg      config.Config
	echo     *echo.Echo
	st       *store.Store
	pool     *fetch.Pool
	obs      *observ.Runtime
	limiters sync.Map
	flood    *FloodGuard
	metrics  *http.Server
}

func New(cfg config.Config, st *store.Store, pool *fetch.Pool, obs *observ.Runtime) *Server {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		code := http.StatusInternalServerError
		msg := "internal error"
		if he, ok := err.(*echo.HTTPError); ok {
			code = he.Code
			if m, ok := he.Message.(string); ok {
				msg = m
			}
		} else if obs != nil {
			obs.CaptureException(err)
		}
		_ = c.JSON(code, map[string]any{"error": msg})
	}
	e.Use(middleware.Recover())
	e.Use(middleware.RequestID())
	e.Use(middleware.Secure())
	e.Use(middleware.BodyLimit(cfg.Server.BodyLimit))
	e.Use(middleware.GzipWithConfig(middleware.GzipConfig{
		Level: 5,
		Skipper: func(c echo.Context) bool {
			return isPublicHealth(c.Request().URL.Path)
		},
	}))

	s := &Server{cfg: cfg, echo: e, st: st, pool: pool, obs: obs}
	if cfg.Security.Maintenance {
		security.SetMaintenance(true)
	}
	s.installMiddleware()
	s.routes()
	return s
}

func (s *Server) routes() {
	s.echo.GET("/healthz", s.healthz)
	s.echo.GET("/livez", s.livez)
	s.echo.GET("/readyz", s.readyz)
	s.echo.GET("/openapi.json", s.openapiJSON)
	s.echo.GET("/docs", s.docsHTML)
	s.echo.GET("/docs/", s.docsHTML)
	s.echo.GET("/v1/stats", s.stats)
	s.echo.GET("/v1/feeds", s.listFeeds)
	s.echo.GET("/v1/feeds/:id", s.getFeed)
	s.echo.POST("/v1/feeds", s.addFeed)
	s.echo.POST("/v1/feeds/:id/refresh", s.refreshFeed)
	s.echo.GET("/v1/search/feeds", s.searchFeeds)
	s.echo.GET("/v1/search/entries", s.searchEntries)
	s.echo.GET("/v1/search/content", s.searchContent)
	s.echo.GET("/v1/similar/feeds/:id", s.similarFeeds)
	s.echo.GET("/v1/entries/:id", s.getEntry)
	s.echo.POST("/v1/entries/:id/fulltext", s.fetchFulltext)
	s.echo.GET("/v1/entries/:id/fulltext", s.getFulltext)
	s.echo.POST("/v1/purge", s.purge)
	s.echo.POST("/v1/discover", s.discover)
	s.echo.POST("/v1/admin/maintenance", s.setMaintenance)
	s.echo.GET("/v1/admin/maintenance", s.getMaintenance)
	s.echo.GET("/v1/admin/tokens", s.listTokens)
	s.echo.POST("/v1/admin/tokens", s.createToken)
	s.echo.DELETE("/v1/admin/tokens/:id", s.revokeToken)
	s.echo.GET("/v1/admin/access-logs", s.accessLogs)
	s.registerFeatureRoutes()
}

func (s *Server) Start() error {
	if s.cfg.Metrics.Addr != "" && s.obs != nil {
		go s.startMetrics()
	}
	ln, err := listenClassic(s.cfg.Server.Addr)
	if err != nil {
		return err
	}
	s.echo.Listener = ln
	srv := &http.Server{
		Addr:              s.cfg.Server.Addr,
		ReadTimeout:       time.Duration(s.cfg.Server.ReadTimeoutSec) * time.Second,
		ReadHeaderTimeout: time.Duration(s.cfg.Server.ReadHeaderTimeoutSec) * time.Second,
		WriteTimeout:      time.Duration(s.cfg.Server.WriteTimeoutSec) * time.Second,
		IdleTimeout:       time.Duration(s.cfg.Server.IdleTimeoutSec) * time.Second,
		MaxHeaderBytes:    s.cfg.Server.MaxHeaderBytes,
	}
	if srv.ReadHeaderTimeout == 0 {
		srv.ReadHeaderTimeout = 5 * time.Second
	}
	if srv.MaxHeaderBytes == 0 {
		srv.MaxHeaderBytes = 64 << 10
	}
	return s.echo.StartServer(srv)
}

func (s *Server) startMetrics() {
	mux := http.NewServeMux()
	mux.HandleFunc(s.cfg.Metrics.Path, func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Metrics.RequireAuth {
			tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if tok == "" {
				tok = r.Header.Get("X-Metrics-Token")
			}
			want := s.cfg.Metrics.AuthToken
			if want == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(want)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		s.obs.MetricsHandler().ServeHTTP(w, r)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"service":"metrics"}`))
	})
	s.metrics = &http.Server{Addr: s.cfg.Metrics.Addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	_ = s.metrics.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.metrics != nil {
		_ = s.metrics.Shutdown(ctx)
	}
	return s.echo.Shutdown(ctx)
}

func listenClassic(addr string) (net.Listener, error) {
	lc := net.ListenConfig{}
	lc.SetMultipathTCP(false)
	return lc.Listen(context.Background(), "tcp", addr)
}

func (s *Server) healthz(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{
		"ok":          true,
		"maintenance": security.Maintenance(),
	})
}

func (s *Server) livez(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{"alive": true})
}

func (s *Server) readyz(c echo.Context) error {
	if err := s.st.DB().PingContext(c.Request().Context()); err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "db not ready")
	}
	return c.JSON(http.StatusOK, map[string]any{"ready": true})
}

func (s *Server) stats(c echo.Context) error {
	if ok, err := s.conditionalGET(c); ok || err != nil {
		return err
	}
	st, err := s.st.Stats(c.Request().Context())
	if err != nil {
		return err
	}
	st["max_total_bytes"] = s.cfg.Limits.MaxTotalBytes
	st["maintenance"] = security.Maintenance()
	return c.JSON(http.StatusOK, st)
}

func (s *Server) listFeeds(c echo.Context) error {
	if ok, err := s.conditionalGET(c); ok || err != nil {
		return err
	}
	limit := s.limit(c)
	offset, _ := strconv.Atoi(c.QueryParam("offset"))
	cat := c.QueryParam("category")
	feeds, err := s.st.ListFeeds(c.Request().Context(), limit, offset, cat)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"feeds": feeds, "count": len(feeds)})
}

func (s *Server) getFeed(c echo.Context) error {
	f, err := s.st.GetFeed(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	if f == nil {
		return echo.NewHTTPError(http.StatusNotFound, "feed not found")
	}
	return c.JSON(http.StatusOK, f)
}

type addFeedReq struct {
	URL      string `json:"url"`
	Title    string `json:"title"`
	Category string `json:"category"`
	Blurb    string `json:"blurb"`
	SiteURL  string `json:"site_url"`
	Refresh  bool   `json:"refresh"`
}

func (s *Server) addFeed(c echo.Context) error {
	var req addFeedReq
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid json")
	}
	req.URL = strings.TrimSpace(req.URL)
	if s.cfg.Security.ValidateUpstreamURL {
		if err := security.ValidatePublicHTTPSURL(req.URL); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
	} else if req.URL == "" || !strings.HasPrefix(req.URL, "http") {
		return echo.NewHTTPError(http.StatusBadRequest, "url required")
	}
	f := store.Feed{
		ID:       store.FeedID(req.URL),
		URL:      req.URL,
		Title:    req.Title,
		Category: req.Category,
		Blurb:    req.Blurb,
		SiteURL:  req.SiteURL,
		Source:   "api",
	}
	if err := s.st.UpsertFeed(c.Request().Context(), f); err != nil {
		return err
	}
	if req.Refresh {
		_ = s.pool.RefreshFeed(c.Request().Context(), f.ID)
	} else {
		s.pool.EnqueueFeed(f.ID)
	}
	out, _ := s.st.GetFeed(c.Request().Context(), f.ID)
	return c.JSON(http.StatusCreated, out)
}

func (s *Server) refreshFeed(c echo.Context) error {
	id := c.Param("id")
	if tokenFromCtx(c) != nil && tokenFromCtx(c).Level == auth.LevelPriority {
		if err := s.pool.RefreshFeed(c.Request().Context(), id); err != nil {
			return err
		}
	} else if !s.pool.EnqueueFeed(id) {
		if err := s.pool.RefreshFeed(c.Request().Context(), id); err != nil {
			return err
		}
	}
	f, _ := s.st.GetFeed(c.Request().Context(), id)
	return c.JSON(http.StatusOK, f)
}

func (s *Server) searchFeeds(c echo.Context) error {
	if ok, err := s.conditionalGET(c); ok || err != nil {
		return err
	}
	q := c.QueryParam("q")
	feeds, err := s.st.SearchFeeds(c.Request().Context(), q, s.limit(c))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"query": q, "feeds": feeds, "count": len(feeds)})
}

func (s *Server) searchEntries(c echo.Context) error {
	q := c.QueryParam("q")
	if q == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "q required")
	}
	entries, err := s.st.SearchEntries(c.Request().Context(), q, s.limit(c))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"query": q, "entries": entries, "count": len(entries)})
}

func (s *Server) searchContent(c echo.Context) error {
	q := c.QueryParam("q")
	if q == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "q required")
	}
	items, err := s.st.SearchFullText(c.Request().Context(), q, s.limit(c))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"query": q, "results": items, "count": len(items)})
}

func (s *Server) similarFeeds(c echo.Context) error {
	method := strings.ToLower(c.QueryParam("method"))
	var feeds []store.Feed
	var err error
	switch method {
	case "embedding", "embed", "vector":
		feeds, err = s.st.SimilarFeedsEmbedding(c.Request().Context(), c.Param("id"), s.limit(c), s.cfg.Search.SimilarityMin)
	default:
		feeds, err = s.st.SimilarFeeds(c.Request().Context(), c.Param("id"), s.limit(c), s.cfg.Search.SimilarityMin)
	}
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"feeds": feeds, "count": len(feeds), "method": firstNonEmpty(method, "jaccard")})
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func (s *Server) getEntry(c echo.Context) error {
	e, err := s.st.GetEntry(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	if e == nil {
		return echo.NewHTTPError(http.StatusNotFound, "entry not found")
	}
	return c.JSON(http.StatusOK, e)
}

func (s *Server) fetchFulltext(c echo.Context) error {
	e, err := s.st.GetEntry(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	if e == nil || e.URL == "" {
		return echo.NewHTTPError(http.StatusNotFound, "entry not found")
	}
	if err := s.pool.FetchArticle(c.Request().Context(), e.ID, e.FeedID, e.URL); err != nil {
		return err
	}
	ft, _ := s.st.GetFullText(c.Request().Context(), e.ID)
	return c.JSON(http.StatusOK, ft)
}

func (s *Server) getFulltext(c echo.Context) error {
	ft, err := s.st.GetFullText(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	if ft == nil {
		return echo.NewHTTPError(http.StatusNotFound, "fulltext not cached")
	}
	return c.JSON(http.StatusOK, ft)
}

func (s *Server) purge(c echo.Context) error {
	res, err := s.st.Purge(
		c.Request().Context(),
		s.cfg.Limits.MaxTotalBytes,
		s.cfg.Limits.MaxFulltextBytes,
		s.cfg.EntryTTL(),
		s.cfg.FulltextTTL(),
		time.Duration(s.cfg.Limits.FaviconTTLHours)*time.Hour,
		s.cfg.Limits.PurgeBatch,
	)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, res)
}

type discoverReq struct {
	URL      string `json:"url"`
	Category string `json:"category"`
	Refresh  bool   `json:"refresh"`
}

func (s *Server) discover(c echo.Context) error {
	var req discoverReq
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid json")
	}
	req.URL = strings.TrimSpace(req.URL)
	if s.cfg.Security.ValidateUpstreamURL {
		if err := security.ValidatePublicHTTPSURL(req.URL); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
	} else if req.URL == "" || !strings.HasPrefix(req.URL, "http") {
		return echo.NewHTTPError(http.StatusBadRequest, "url required")
	}
	f := store.Feed{
		ID:       store.FeedID(req.URL),
		URL:      req.URL,
		Category: req.Category,
		Source:   "discover",
		Blurb:    "User discovery",
	}
	if err := s.st.UpsertFeed(c.Request().Context(), f); err != nil {
		return err
	}
	if req.Refresh || (tokenFromCtx(c) != nil && tokenFromCtx(c).Level == auth.LevelPriority) {
		_ = s.pool.RefreshFeed(c.Request().Context(), f.ID)
	} else {
		s.pool.EnqueueFeed(f.ID)
	}
	out, _ := s.st.GetFeed(c.Request().Context(), f.ID)
	return c.JSON(http.StatusAccepted, out)
}

type maintReq struct {
	Enabled bool `json:"enabled"`
}

func (s *Server) setMaintenance(c echo.Context) error {
	var req maintReq
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid json")
	}
	security.SetMaintenance(req.Enabled)
	return c.JSON(http.StatusOK, map[string]any{"maintenance": security.Maintenance()})
}

func (s *Server) getMaintenance(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{"maintenance": security.Maintenance()})
}

type createTokenReq struct {
	Name  string `json:"name"`
	Level string `json:"level"`
	TTL   string `json:"ttl"`
}

func (s *Server) createToken(c echo.Context) error {
	var req createTokenReq
	if err := c.Bind(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "name required")
	}
	lv, err := auth.ParseLevel(req.Level)
	if err != nil {
		lv = auth.LevelStandard
	}
	var ttl time.Duration
	if req.TTL != "" {
		ttl, err = time.ParseDuration(req.TTL)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid ttl")
		}
	}
	plain, rec, err := auth.Generate(lv, req.Name, ttl)
	if err != nil {
		return err
	}
	if err := s.st.CreateToken(c.Request().Context(), rec); err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, map[string]any{
		"id":    rec.ID,
		"name":  rec.Name,
		"level": rec.Level,
		"token": plain,
	})
}

func (s *Server) listTokens(c echo.Context) error {
	include := c.QueryParam("revoked") == "1" || c.QueryParam("revoked") == "true"
	list, err := s.st.ListTokens(c.Request().Context(), include)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"tokens": list, "count": len(list)})
}

func (s *Server) revokeToken(c echo.Context) error {
	ok, err := s.st.RevokeToken(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	if !ok {
		return echo.NewHTTPError(http.StatusNotFound, "token not found")
	}
	return c.JSON(http.StatusOK, map[string]any{"revoked": true, "id": c.Param("id")})
}

func (s *Server) accessLogs(c echo.Context) error {
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	if limit < 1 {
		limit = 50
	}
	logs, err := s.st.ListAccessLogs(c.Request().Context(), c.QueryParam("token_id"), limit)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"logs": logs, "count": len(logs)})
}

func (s *Server) limit(c echo.Context) int {
	n := s.cfg.Search.DefaultLimit
	if v := c.QueryParam("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			n = parsed
		}
	}
	max := s.cfg.Search.MaxLimit
	if rec := tokenFromCtx(c); rec != nil && rec.Level == auth.LevelPriority {
		max = max * 2
	}
	if n < 1 {
		n = 1
	}
	if n > max {
		n = max
	}
	return n
}

func (s *Server) Echo() *echo.Echo { return s.echo }
