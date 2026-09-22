package api

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Quad4-Software/rss-discovery/internal/auth"
	"github.com/Quad4-Software/rss-discovery/internal/security"
	"github.com/labstack/echo/v4"
)

// FloodGuard rejects modern application-layer abuse cheaply before handlers run.
type FloodGuard struct {
	maxBody           int64
	maxConcurrent     int64
	maxConnPerIP      int
	readHeaderTimeout time.Duration
	inFlight          atomic.Int64
	ipConns           sync.Map // ip -> *atomic.Int64
}

func newFloodGuard(maxBody int64, maxConcurrent int64, maxConnPerIP int) *FloodGuard {
	if maxBody < 1 {
		maxBody = 4 << 20
	}
	if maxConcurrent < 1 {
		maxConcurrent = 4096
	}
	if maxConnPerIP < 1 {
		maxConnPerIP = 64
	}
	return &FloodGuard{
		maxBody:       maxBody,
		maxConcurrent: maxConcurrent,
		maxConnPerIP:  maxConnPerIP,
	}
}

// publicConcurrencyLimit reserves slots for authenticated clients.
func publicConcurrencyLimit(maxConcurrent, reserved int64) int64 {
	if maxConcurrent < 1 {
		return 1
	}
	if reserved <= 0 {
		return maxConcurrent
	}
	if reserved >= maxConcurrent {
		reserved = maxConcurrent - 1
	}
	anonCap := maxConcurrent - reserved
	if anonCap < 1 {
		return 1
	}
	return anonCap
}

func (s *Server) mwFlood(next echo.HandlerFunc) echo.HandlerFunc {
	fg := s.flood
	if fg == nil {
		return next
	}
	return func(c echo.Context) error {
		r := c.Request()

		// Cap path and query before any handler work (ReDoS / parser abuse).
		if len(r.URL.Path) > 2048 || len(r.URL.RawQuery) > 4096 {
			return echo.NewHTTPError(http.StatusRequestURITooLong, "uri too long")
		}

		// Reject ambiguous HTTP/1.1 framing (request smuggling probes).
		if te := r.Header.Get("Transfer-Encoding"); te != "" {
			if strings.Contains(strings.ToLower(te), "chunked") && r.Header.Get("Content-Length") != "" {
				return echo.NewHTTPError(http.StatusBadRequest, "ambiguous framing")
			}
			if strings.Count(strings.ToLower(te), "chunked") > 1 {
				return echo.NewHTTPError(http.StatusBadRequest, "ambiguous framing")
			}
		}
		if cl := r.Header.Values("Content-Length"); len(cl) > 1 {
			return echo.NewHTTPError(http.StatusBadRequest, "ambiguous framing")
		}

		// Oversized declared body.
		if r.ContentLength > fg.maxBody {
			return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "payload too large")
		}
		if r.Body != nil && r.ContentLength >= 0 {
			r.Body = http.MaxBytesReader(c.Response(), r.Body, fg.maxBody)
		} else if r.Body != nil {
			r.Body = http.MaxBytesReader(c.Response(), r.Body, fg.maxBody)
		}

		hasCreds := extractToken(c, s.cfg.Auth.Header, s.cfg.Auth.QueryParam) != ""
		health := isHealthOnly(r.URL.Path)

		// Global concurrency shed. Reserve capacity for authed clients so public
		// traffic is shed first under load.
		cur := fg.inFlight.Add(1)
		defer fg.inFlight.Add(-1)
		limit := fg.maxConcurrent
		if !hasCreds && !health {
			limit = publicConcurrencyLimit(fg.maxConcurrent, s.cfg.Auth.AuthedReservedConcurrent)
		}
		if cur > limit {
			c.Response().Header().Set("Retry-After", "1")
			if !hasCreds {
				c.Response().Header().Set("X-Shed-Reason", "public-capacity")
			}
			return echo.NewHTTPError(http.StatusServiceUnavailable, "overloaded")
		}

		ip := security.ClientIP(r, s.cfg.Security.TrustProxy)
		if security.Global().MatchIP(ip) {
			return echo.NewHTTPError(http.StatusForbidden, "forbidden")
		}
		v, _ := fg.ipConns.LoadOrStore(ip, &atomic.Int64{})
		counter := v.(*atomic.Int64)
		n := counter.Add(1)
		defer counter.Add(-1)
		maxConn := fg.maxConnPerIP
		if !hasCreds && !health {
			if pub := s.cfg.Auth.PublicMaxConnPerIP; pub > 0 && pub < maxConn {
				maxConn = pub
			}
		}
		if n > int64(maxConn) {
			c.Response().Header().Set("Retry-After", "2")
			return echo.NewHTTPError(http.StatusTooManyRequests, "too many connections")
		}

		// Cheap method allowlist for public surface.
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodDelete, http.MethodOptions:
		default:
			return echo.NewHTTPError(http.StatusMethodNotAllowed, "method not allowed")
		}

		err := next(c)
		return err
	}
}

func (s *Server) mwRateLimitHeaders(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		err := next(c)
		if he, ok := err.(*echo.HTTPError); ok && he.Code == http.StatusTooManyRequests {
			c.Response().Header().Set("Retry-After", "1")
			c.Response().Header().Set("X-RateLimit-Policy", "token-bucket")
		}
		return err
	}
}

func (s *Server) mwNoStore(next echo.HandlerFunc) echo.HandlerFunc {
	return s.mwCacheHeaders(next)
}

func (s *Server) mwCacheHeaders(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		path := c.Request().URL.Path
		method := c.Request().Method
		rec, _ := c.Get(ctxToken).(*auth.TokenRecord)
		hasToken := rec != nil
		queryAuth := s.cfg.Auth.QueryParam != "" && strings.TrimSpace(c.QueryParam(s.cfg.Auth.QueryParam)) != ""
		authed := hasToken || queryAuth ||
			c.Request().Header.Get("Authorization") != "" ||
			c.Request().Header.Get("X-API-Key") != ""
		if method == http.MethodGet || method == http.MethodHead {
			// Never mark authenticated API responses as publicly cacheable.
			if authed {
				c.Response().Header().Set("Cache-Control", "private, no-store")
			} else if s.cfg.Cache.CDNEnabled && isReadonlyCacheable(path) {
				maxAge := s.cfg.Cache.MaxAgeSec
				if maxAge < 1 {
					maxAge = 30
				}
				swr := s.cfg.Cache.StaleWhileRevalidate
				if swr < 0 {
					swr = 0
				}
				c.Response().Header().Set("Cache-Control",
					"public, max-age="+strconv.Itoa(maxAge)+", stale-while-revalidate="+strconv.Itoa(swr))
				c.Response().Header().Set("Vary", "Authorization, Accept-Encoding")
			} else if path != "/openapi.json" && path != "/docs" && path != "/docs/" &&
				!strings.HasPrefix(path, "/docs/assets/") {
				c.Response().Header().Set("Cache-Control", "private, no-store")
			}
		} else {
			c.Response().Header().Set("Cache-Control", "private, no-store")
		}
		return next(c)
	}
}

func isReadonlyCacheable(path string) bool {
	switch path {
	case "/v1/stats", "/v1/feeds", "/openapi.json", "/docs", "/docs/":
		return true
	default:
		if strings.HasPrefix(path, "/v1/search/") || strings.HasPrefix(path, "/v1/similar/") {
			return true
		}
		// Single feed resource only: /v1/feeds/{id} with no further segments.
		if strings.HasPrefix(path, "/v1/feeds/") {
			rest := strings.TrimPrefix(path, "/v1/feeds/")
			if rest != "" && !strings.Contains(rest, "/") {
				return true
			}
		}
		return false
	}
}

func (s *Server) mwCORS(next echo.HandlerFunc) echo.HandlerFunc {
	if !s.cfg.CORS.Enabled {
		return next
	}
	origins := s.cfg.CORS.AllowOrigins
	if len(origins) == 0 {
		origins = []string{"*"}
	}
	headers := strings.Join(s.cfg.CORS.AllowHeaders, ", ")
	if headers == "" {
		headers = "Authorization, Content-Type, X-API-Key"
	}
	methods := strings.Join(s.cfg.CORS.AllowMethods, ", ")
	if methods == "" {
		methods = "GET, POST, DELETE, OPTIONS, HEAD"
	}
	maxAge := s.cfg.CORS.MaxAgeSec
	if maxAge < 1 {
		maxAge = 600
	}
	return func(c echo.Context) error {
		origin := c.Request().Header.Get("Origin")
		allowOrigin := ""
		for _, o := range origins {
			if o == "*" {
				// Browsers reject ACAO:* with credentials. Never reflect arbitrary Origin.
				if s.cfg.CORS.AllowCredentials {
					continue
				}
				allowOrigin = "*"
				break
			}
			if o == origin && origin != "" {
				allowOrigin = o
				break
			}
		}
		if allowOrigin != "" {
			c.Response().Header().Set("Access-Control-Allow-Origin", allowOrigin)
			c.Response().Header().Set("Access-Control-Allow-Headers", headers)
			c.Response().Header().Set("Access-Control-Allow-Methods", methods)
			c.Response().Header().Set("Access-Control-Max-Age", strconv.Itoa(maxAge))
			if s.cfg.CORS.AllowCredentials && allowOrigin != "*" {
				c.Response().Header().Set("Access-Control-Allow-Credentials", "true")
			}
			c.Response().Header().Add("Vary", "Origin")
		}
		if c.Request().Method == http.MethodOptions {
			return c.NoContent(http.StatusNoContent)
		}
		return next(c)
	}
}
