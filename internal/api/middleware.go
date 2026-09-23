package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Quad4-Software/rss-discovery/internal/auth"
	"github.com/Quad4-Software/rss-discovery/internal/security"
	"github.com/Quad4-Software/rss-discovery/internal/store"
	"github.com/labstack/echo/v4"
	"golang.org/x/time/rate"
)

type ctxKey string

const (
	ctxToken = "api_token"
	ctxLevel = "api_level"
)

type limiterEntry struct {
	lim  *rate.Limiter
	last time.Time
}

// MiddlewareStack installs security, auth, rate limit, and access logging.
func (s *Server) installMiddleware() {
	s.limiters = sync.Map{}
	s.flood = newFloodGuard(parseBodyLimit(s.cfg.Server.BodyLimit), s.cfg.Server.MaxConcurrent, s.cfg.Server.MaxConnPerIP)
	s.echo.Use(s.mwFlood)
	s.echo.Use(s.mwCORS)
	s.echo.Use(s.mwSecurity)
	s.echo.Use(s.mwIPRateLimit)
	s.echo.Use(s.mwAuth)
	// Cache after auth so 401s are never advertised as CDN-public and query-param auth is visible.
	s.echo.Use(s.mwCacheHeaders)
	s.echo.Use(s.mwMaintenance)
	s.echo.Use(s.mwTokenRateLimit)
	s.echo.Use(s.mwRateLimitHeaders)
	s.echo.Use(s.mwMetrics)
	s.echo.Use(s.mwAccessLog)
}

func parseBodyLimit(s string) int64 {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 4 << 20
	}
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "KB"):
		mult = 1 << 10
		s = strings.TrimSuffix(s, "KB")
	case strings.HasSuffix(s, "MB"), strings.HasSuffix(s, "M"):
		mult = 1 << 20
		s = strings.TrimSuffix(strings.TrimSuffix(s, "MB"), "M")
	case strings.HasSuffix(s, "GB"), strings.HasSuffix(s, "G"):
		mult = 1 << 30
		s = strings.TrimSuffix(strings.TrimSuffix(s, "GB"), "G")
	case strings.HasSuffix(s, "B"):
		s = strings.TrimSuffix(s, "B")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 1 {
		return 4 << 20
	}
	return n * mult
}

func (s *Server) mwSecurity(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		path := c.Request().URL.Path
		ua := c.Request().UserAgent()
		if s.cfg.Security.BlockProbes && security.IsProbePath(path) {
			return echo.NewHTTPError(http.StatusNotFound, "not found")
		}
		if security.IsBlocklistedUA(ua) {
			return echo.NewHTTPError(http.StatusForbidden, "forbidden")
		}
		if s.cfg.Security.BlockScanners && security.IsScannerUA(ua) {
			return echo.NewHTTPError(http.StatusForbidden, "forbidden")
		}
		if s.cfg.Security.BlockScrapers && security.IsScraperUA(ua) {
			return echo.NewHTTPError(http.StatusForbidden, "forbidden")
		}
		if s.cfg.Security.RequireUA && security.EmptyOrGenericUA(ua) {
			if !isPublicHealth(path) {
				return echo.NewHTTPError(http.StatusBadRequest, "user-agent required")
			}
		}
		return next(c)
	}
}

func (s *Server) mwMaintenance(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if !security.Maintenance() {
			return next(c)
		}
		path := c.Request().URL.Path
		if isPublicHealth(path) {
			return next(c)
		}
		level, _ := c.Get(ctxLevel).(auth.Level)
		if level == auth.LevelAdmin {
			return next(c)
		}
		c.Response().Header().Set("Retry-After", "120")
		return echo.NewHTTPError(http.StatusServiceUnavailable, "maintenance mode")
	}
}

func (s *Server) mwAuth(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		path := c.Request().URL.Path
		method := c.Request().Method
		if isPublicPath(path) {
			return next(c)
		}
		raw := extractToken(c, s.cfg.Auth.Header, s.cfg.Auth.QueryParam)
		if raw == "" {
			if s.cfg.Auth.Required {
				return echo.NewHTTPError(http.StatusUnauthorized, "api token required")
			}
			// Public mode: anonymous clients get readonly privileges only.
			if !auth.Allows(auth.LevelReadonly, method, path) {
				return echo.NewHTTPError(http.StatusUnauthorized, "api token required")
			}
			return next(c)
		}
		hash := auth.HashToken(raw)
		rec, err := s.st.LookupTokenByHash(c.Request().Context(), hash)
		if err != nil {
			return err
		}
		if rec == nil || store.ActiveToken(rec) != nil {
			// Try OAuth access token.
			orec, oerr := s.st.LookupOAuthToken(c.Request().Context(), raw)
			if oerr != nil {
				return oerr
			}
			if orec == nil {
				return echo.NewHTTPError(http.StatusUnauthorized, "invalid token")
			}
			rec = orec
		}
		if !auth.Allows(rec.Level, method, path) {
			return echo.NewHTTPError(http.StatusForbidden, "insufficient privileges")
		}
		c.Set(ctxToken, rec)
		c.Set(ctxLevel, rec.Level)
		id := rec.ID
		if !strings.HasPrefix(rec.Name, "oauth:") {
			go func() {
				_ = s.st.TouchToken(context.Background(), id)
			}()
		}
		return next(c)
	}
}

func extractToken(c echo.Context, header, query string) string {
	if header == "" {
		header = "Authorization"
	}
	h := c.Request().Header.Get(header)
	if h != "" {
		if strings.HasPrefix(strings.ToLower(h), "bearer ") {
			return strings.TrimSpace(h[7:])
		}
		return strings.TrimSpace(h)
	}
	if x := c.Request().Header.Get("X-API-Key"); x != "" {
		return strings.TrimSpace(x)
	}
	if query != "" {
		return strings.TrimSpace(c.QueryParam(query))
	}
	return ""
}

func (s *Server) mwIPRateLimit(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		path := c.Request().URL.Path
		// Health probes stay unrestricted. OAuth token and WebSub callback are rate-limited.
		if isHealthOnly(path) {
			return next(c)
		}
		// Authed clients skip IP buckets and use per-token limits instead.
		if extractToken(c, s.cfg.Auth.Header, s.cfg.Auth.QueryParam) != "" {
			return next(c)
		}
		ip := security.ClientIP(c.Request(), s.cfg.Security.TrustProxy)
		perMin, burst := auth.AnonymousRateLimit(s.cfg.Auth.PublicRatePerMin, s.cfg.Auth.PublicBurst)
		if path == "/oauth/token" {
			perMin, burst = 30, 5
		}
		lim := s.getLimiter("ip:"+ip, perMin, burst)
		if !lim.Allow() {
			c.Response().Header().Set("Retry-After", "1")
			c.Response().Header().Set("X-RateLimit-Scope", "public")
			return echo.NewHTTPError(http.StatusTooManyRequests, "rate limit exceeded")
		}
		return next(c)
	}
}

func (s *Server) mwTokenRateLimit(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if isPublicHealth(c.Request().URL.Path) {
			return next(c)
		}
		rec, ok := c.Get(ctxToken).(*auth.TokenRecord)
		if !ok || rec == nil {
			return next(c)
		}
		perMin, burst := auth.RateLimit(rec.Level)
		lim := s.getLimiter("tok:"+rec.ID, perMin, burst)
		if !lim.Allow() {
			c.Response().Header().Set("Retry-After", "1")
			c.Response().Header().Set("X-RateLimit-Scope", "token")
			return echo.NewHTTPError(http.StatusTooManyRequests, "rate limit exceeded")
		}
		return next(c)
	}
}

func (s *Server) getLimiter(key string, perMin, burst int) *rate.Limiter {
	if v, ok := s.limiters.Load(key); ok {
		e := v.(*limiterEntry)
		e.last = time.Now()
		return e.lim
	}
	rps := rate.Limit(float64(perMin) / 60.0)
	if rps <= 0 {
		rps = 1
	}
	lim := rate.NewLimiter(rps, burst)
	s.limiters.Store(key, &limiterEntry{lim: lim, last: time.Now()})
	return lim
}

func (s *Server) mwMetrics(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if s.obs == nil {
			return next(c)
		}
		s.obs.InFlight.Inc()
		start := time.Now()
		err := next(c)
		s.obs.InFlight.Dec()
		status := c.Response().Status
		if status == 0 {
			status = 200
		}
		level := "anonymous"
		if l, ok := c.Get(ctxLevel).(auth.Level); ok {
			level = string(l)
		}
		path := routeLabel(c)
		s.obs.Requests.WithLabelValues(c.Request().Method, path, strconv.Itoa(status), level).Inc()
		s.obs.Latency.WithLabelValues(c.Request().Method, path).Observe(time.Since(start).Seconds())
		return err
	}
}

func (s *Server) mwAccessLog(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		start := time.Now()
		err := next(c)
		if !s.cfg.Observ.AccessLog {
			return err
		}
		status := c.Response().Status
		if he, ok := err.(*echo.HTTPError); ok && status == 0 {
			status = he.Code
		}
		if status == 0 {
			status = 200
		}
		tokenID := ""
		level := ""
		if rec, ok := c.Get(ctxToken).(*auth.TokenRecord); ok && rec != nil {
			tokenID = rec.ID
			level = string(rec.Level)
		}
		log := auth.AccessLog{
			TokenID:   tokenID,
			Level:     level,
			Method:    c.Request().Method,
			Path:      c.Path(),
			Status:    status,
			IP:        security.ClientIP(c.Request(), s.cfg.Security.TrustProxy),
			UA:        c.Request().UserAgent(),
			LatencyMs: time.Since(start).Milliseconds(),
			CreatedAt: time.Now().UTC(),
		}
		if log.Path == "" {
			// Strip query string to avoid leaking api_key.
			p := c.Request().URL.Path
			log.Path = p
		}
		if s.cfg.Observ.PrivacyMode {
			log.IP = hashIP(log.IP)
		}
		go func(log auth.AccessLog) {
			_ = s.st.InsertAccessLog(context.Background(), log)
		}(log)
		return err
	}
}

func isPublicHealth(path string) bool {
	return isPublicPath(path)
}

func isHealthOnly(path string) bool {
	switch path {
	case "/healthz", "/readyz", "/livez":
		return true
	default:
		return false
	}
}

func isPublicPath(path string) bool {
	switch path {
	case "/", "/healthz", "/readyz", "/livez", "/openapi.json", "/docs", "/docs/",
		"/oauth/token", "/v1/websub/callback":
		return true
	default:
		// vendored scalar bundle and fonts backing /docs
		return strings.HasPrefix(path, "/docs/assets/")
	}
}

func routeLabel(c echo.Context) string {
	if p := c.Path(); p != "" {
		return p
	}
	return c.Request().URL.Path
}

func hashIP(ip string) string {
	if ip == "" {
		return ""
	}
	sum := auth.HashToken(ip)
	if len(sum) > 12 {
		return sum[:12]
	}
	return sum
}

func tokenFromCtx(c echo.Context) *auth.TokenRecord {
	rec, _ := c.Get(ctxToken).(*auth.TokenRecord)
	return rec
}
