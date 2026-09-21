package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/Quad4-Software/rss-discovery/internal/auth"
	"github.com/Quad4-Software/rss-discovery/internal/opml"
	"github.com/Quad4-Software/rss-discovery/internal/security"
	"github.com/Quad4-Software/rss-discovery/internal/store"
	"github.com/Quad4-Software/rss-discovery/internal/websub"
)

func (s *Server) registerFeatureRoutes() {
	s.echo.GET("/v1/opml", s.exportOPML)
	s.echo.POST("/v1/opml", s.importOPML)
	s.echo.POST("/v1/discover/bulk", s.bulkDiscover)
	s.echo.GET("/v1/jobs", s.listJobs)
	s.echo.GET("/v1/jobs/:id", s.getJob)

	s.echo.GET("/v1/feeds/:id/status", s.feedStatus)
	s.echo.GET("/v1/feeds/:id/health", s.feedHealth)
	s.echo.GET("/v1/status/freshness", s.freshnessSummary)
	s.echo.GET("/v1/health/feeds", s.listFeedHealth)

	s.echo.GET("/v1/webhooks", s.listWebhooks)
	s.echo.POST("/v1/webhooks", s.createWebhook)
	s.echo.DELETE("/v1/webhooks/:id", s.deleteWebhook)

	s.echo.GET("/v1/websub", s.listWebSub)
	s.echo.POST("/v1/websub/:id/subscribe", s.subscribeWebSub)
	s.echo.DELETE("/v1/websub/:id", s.unsubscribeWebSub)
	s.echo.GET("/v1/websub/callback", s.websubCallback)
	s.echo.POST("/v1/websub/callback", s.websubCallback)

	s.echo.POST("/oauth/token", s.oauthToken)
	s.echo.GET("/v1/admin/oauth/clients", s.listOAuthClients)
	s.echo.POST("/v1/admin/oauth/clients", s.createOAuthClient)
	s.echo.DELETE("/v1/admin/oauth/clients/:id", s.revokeOAuthClient)
}

func (s *Server) exportOPML(c echo.Context) error {
	feeds, err := s.st.ListFeeds(c.Request().Context(), s.cfg.Search.MaxLimit*10, 0, c.QueryParam("category"))
	if err != nil {
		return err
	}
	items := make([]opml.Outline, 0, len(feeds))
	for _, f := range feeds {
		items = append(items, opml.Outline{
			Title: f.Title, XMLURL: f.URL, HTMLURL: f.SiteURL, Category: f.Category, Description: f.Description,
		})
	}
	data, err := opml.Export("RSS Discovery", items)
	if err != nil {
		return err
	}
	return c.Blob(http.StatusOK, "text/x-opml+xml; charset=utf-8", data)
}

func (s *Server) importOPML(c echo.Context) error {
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, 8<<20))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "read body")
	}
	items, err := opml.Parse(body)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	const maxOPMLFeeds = 500
	if len(items) > maxOPMLFeeds {
		return echo.NewHTTPError(http.StatusBadRequest, "max 500 feeds in opml")
	}
	urls := make([]string, 0, len(items))
	for _, it := range items {
		if s.cfg.Security.ValidateUpstreamURL {
			if err := security.ValidatePublicHTTPSURL(it.XMLURL); err != nil {
				continue
			}
		}
		urls = append(urls, it.XMLURL)
		_ = s.st.UpsertFeed(c.Request().Context(), store.Feed{
			ID: store.FeedID(it.XMLURL), URL: it.XMLURL, Title: it.Title,
			SiteURL: it.HTMLURL, Category: it.Category, Description: it.Description, Source: "opml",
		})
	}
	createdBy := ""
	if rec := tokenFromCtx(c); rec != nil {
		createdBy = rec.ID
	}
	job, err := s.st.CreateJob(c.Request().Context(), store.JobOPMLImport, createdBy, urls)
	if err != nil {
		return err
	}
	go s.runJob(job.ID)
	return c.JSON(http.StatusAccepted, job)
}

type bulkDiscoverReq struct {
	URLs    []string `json:"urls"`
	Refresh bool     `json:"refresh"`
}

func (s *Server) bulkDiscover(c echo.Context) error {
	var req bulkDiscoverReq
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid json")
	}
	if len(req.URLs) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "urls required")
	}
	if len(req.URLs) > 500 {
		return echo.NewHTTPError(http.StatusBadRequest, "max 500 urls")
	}
	urls := make([]string, 0, len(req.URLs))
	for _, u := range req.URLs {
		u = strings.TrimSpace(u)
		if s.cfg.Security.ValidateUpstreamURL {
			if err := security.ValidatePublicHTTPSURL(u); err != nil {
				continue
			}
		} else if !strings.HasPrefix(u, "http") {
			continue
		}
		urls = append(urls, u)
		_ = s.st.UpsertFeed(c.Request().Context(), store.Feed{
			ID: store.FeedID(u), URL: u, Source: "bulk", Blurb: "Bulk discovery",
		})
	}
	createdBy := ""
	if rec := tokenFromCtx(c); rec != nil {
		createdBy = rec.ID
	}
	job, err := s.st.CreateJob(c.Request().Context(), store.JobBulkDiscover, createdBy, urls)
	if err != nil {
		return err
	}
	go s.runJob(job.ID)
	return c.JSON(http.StatusAccepted, job)
}

func (s *Server) runJob(jobID string) {
	ctx := context.Background()
	_, _ = s.st.ReclaimStaleJobItems(ctx, 15*time.Minute)
	_ = s.st.SetJobRunning(ctx, jobID)
	for {
		items, err := s.st.ClaimPendingItems(ctx, jobID, 8)
		if err != nil || len(items) == 0 {
			break
		}
		for _, it := range items {
			fid := store.FeedID(it.URL)
			if err := s.pool.RefreshFeed(ctx, fid); err != nil {
				_ = s.st.MarkItemFailed(ctx, it.ID, err.Error())
				continue
			}
			_ = s.st.MarkItemDone(ctx, it.ID, fid)
		}
	}
	remaining, _ := s.st.JobPendingRemaining(ctx, jobID)
	if remaining == 0 {
		_ = s.st.CompleteJob(ctx, jobID, "")
	}
}

func (s *Server) listJobs(c echo.Context) error {
	createdBy := ""
	if rec := tokenFromCtx(c); rec != nil && rec.Level != auth.LevelAdmin {
		createdBy = rec.ID
	}
	list, err := s.st.ListJobsByCreator(c.Request().Context(), createdBy, 50)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"jobs": list, "count": len(list)})
}

func (s *Server) getJob(c echo.Context) error {
	j, err := s.st.GetJob(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	if j == nil {
		return echo.NewHTTPError(http.StatusNotFound, "job not found")
	}
	if rec := tokenFromCtx(c); rec != nil && rec.Level != auth.LevelAdmin {
		if j.CreatedBy == "" || j.CreatedBy != rec.ID {
			return echo.NewHTTPError(http.StatusNotFound, "job not found")
		}
	}
	items, _ := s.st.ListJobItems(c.Request().Context(), j.ID, 200)
	return c.JSON(http.StatusOK, map[string]any{"job": j, "items": items})
}

func (s *Server) feedStatus(c echo.Context) error {
	st, err := s.st.GetFeedStatus(c.Request().Context(), c.Param("id"), s.cfg.FreshnessSLO())
	if err != nil {
		return err
	}
	if st == nil {
		return echo.NewHTTPError(http.StatusNotFound, "feed not found")
	}
	return c.JSON(http.StatusOK, st)
}

func (s *Server) feedHealth(c echo.Context) error {
	h, err := s.st.GetFeedHealth(c.Request().Context(), c.Param("id"), s.cfg.FreshnessSLO())
	if err != nil {
		return err
	}
	if h == nil {
		return echo.NewHTTPError(http.StatusNotFound, "feed not found")
	}
	return c.JSON(http.StatusOK, h)
}

func (s *Server) freshnessSummary(c echo.Context) error {
	sum, err := s.st.FreshnessSummary(c.Request().Context(), s.cfg.FreshnessSLO())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, sum)
}

func (s *Server) listFeedHealth(c echo.Context) error {
	list, err := s.st.ListFeedHealth(c.Request().Context(), s.limit(c), s.cfg.FreshnessSLO())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"feeds": list, "count": len(list)})
}

type webhookReq struct {
	URL    string `json:"url"`
	Events string `json:"events"`
	FeedID string `json:"feed_id"`
}

func (s *Server) createWebhook(c echo.Context) error {
	var req webhookReq
	if err := c.Bind(&req); err != nil || strings.TrimSpace(req.URL) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "url required")
	}
	if err := security.ValidatePublicHTTPSURL(req.URL); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	createdBy := ""
	if rec := tokenFromCtx(c); rec != nil {
		createdBy = rec.ID
	}
	wh, secret, err := s.st.CreateWebhook(c.Request().Context(), req.URL, req.Events, req.FeedID, createdBy)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, map[string]any{
		"id": wh.ID, "url": wh.URL, "events": wh.Events, "feed_id": wh.FeedID,
		"secret": secret, "active": wh.Active,
	})
}

func (s *Server) listWebhooks(c echo.Context) error {
	var (
		list []store.Webhook
		err  error
	)
	if rec := tokenFromCtx(c); rec != nil && rec.Level != auth.LevelAdmin {
		list, err = s.st.ListWebhooksByCreator(c.Request().Context(), rec.ID)
	} else {
		list, err = s.st.ListWebhooks(c.Request().Context())
	}
	if err != nil {
		return err
	}
	type pub struct {
		ID         string `json:"id"`
		URL        string `json:"url"`
		Events     string `json:"events"`
		FeedID     string `json:"feed_id,omitempty"`
		Active     bool   `json:"active"`
		FailCount  int    `json:"fail_count"`
		LastStatus int    `json:"last_status"`
	}
	out := make([]pub, 0, len(list))
	for _, w := range list {
		out = append(out, pub{ID: w.ID, URL: w.URL, Events: w.Events, FeedID: w.FeedID, Active: w.Active, FailCount: w.FailCount, LastStatus: w.LastStatus})
	}
	return c.JSON(http.StatusOK, map[string]any{"webhooks": out, "count": len(out)})
}

func (s *Server) deleteWebhook(c echo.Context) error {
	id := c.Param("id")
	var (
		ok  bool
		err error
	)
	if rec := tokenFromCtx(c); rec != nil && rec.Level != auth.LevelAdmin {
		ok, err = s.st.DeleteWebhookByCreator(c.Request().Context(), id, rec.ID)
	} else {
		ok, err = s.st.DeleteWebhook(c.Request().Context(), id)
	}
	if err != nil {
		return err
	}
	if !ok {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	return c.JSON(http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) listWebSub(c echo.Context) error {
	list, err := s.st.ListWebSubs(c.Request().Context(), 200)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"subscriptions": list, "count": len(list)})
}

func (s *Server) subscribeWebSub(c echo.Context) error {
	feedID := c.Param("id")
	f, err := s.st.GetFeed(c.Request().Context(), feedID)
	if err != nil || f == nil {
		return echo.NewHTTPError(http.StatusNotFound, "feed not found")
	}
	if s.cfg.WebSub.CallbackURL == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "websub.callback_url not configured")
	}
	// Trigger refresh which discovers hub.
	_ = s.pool.RefreshFeed(c.Request().Context(), f.ID)
	sub, _ := s.st.GetWebSubByFeed(c.Request().Context(), f.ID)
	return c.JSON(http.StatusAccepted, map[string]any{"feed_id": f.ID, "subscription": sub})
}

func (s *Server) unsubscribeWebSub(c echo.Context) error {
	feedID := c.Param("id")
	sub, err := s.st.GetWebSubByFeed(c.Request().Context(), feedID)
	if err != nil {
		return err
	}
	if sub != nil && s.cfg.WebSub.CallbackURL != "" {
		cli := &websub.Client{Callback: s.cfg.WebSub.CallbackURL, LeaseSecs: s.cfg.WebSub.LeaseSeconds}
		_ = cli.Unsubscribe(c.Request().Context(), sub.Hub, sub.Topic, sub.Secret)
	}
	_ = s.st.DeleteWebSubByFeed(c.Request().Context(), feedID)
	return c.JSON(http.StatusOK, map[string]any{"unsubscribed": true})
}

func (s *Server) websubCallback(c echo.Context) error {
	// Hub verification is GET with hub.mode + hub.challenge.
	if c.Request().Method == http.MethodGet {
		mode := c.QueryParam("hub.mode")
		topic := c.QueryParam("hub.topic")
		challenge := c.QueryParam("hub.challenge")
		lease := c.QueryParam("hub.lease_seconds")
		if topic == "" || challenge == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "missing hub params")
		}
		sub, err := s.st.GetWebSubByTopic(c.Request().Context(), topic)
		if err != nil {
			return err
		}
		if sub == nil {
			return echo.NewHTTPError(http.StatusNotFound, "unknown topic")
		}
		switch mode {
		case "denied":
			sub.Status = store.WebSubDenied
			sub.LastError = c.QueryParam("hub.reason")
			_ = s.st.UpsertWebSub(c.Request().Context(), *sub)
			return c.NoContent(http.StatusOK)
		case "subscribe", "unsubscribe":
			if mode == "subscribe" {
				sub.Status = store.WebSubActive
				if secs, err := strconv.Atoi(lease); err == nil && secs > 0 {
					exp := time.Now().UTC().Add(time.Duration(secs) * time.Second)
					sub.ExpiresAt = &exp
					sub.LeaseSecs = secs
				}
			} else {
				sub.Status = store.WebSubExpired
			}
			_ = s.st.UpsertWebSub(c.Request().Context(), *sub)
			return c.String(http.StatusOK, challenge)
		default:
			return echo.NewHTTPError(http.StatusBadRequest, "unknown hub.mode")
		}
	}

	body, err := io.ReadAll(io.LimitReader(c.Request().Body, s.cfg.Workers.MaxFeedBytes))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad body")
	}
	topic := c.Request().Header.Get("X-Hub-Topic")
	if topic == "" {
		topic = c.QueryParam("hub.topic")
	}
	if topic == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "missing topic")
	}
	sub, _ := s.st.GetWebSubByTopic(c.Request().Context(), topic)
	if sub == nil || sub.Secret == "" {
		return echo.NewHTTPError(http.StatusNotFound, "unknown topic")
	}
	sig := c.Request().Header.Get("X-Hub-Signature")
	if !websub.VerifySignature(sub.Secret, body, sig) {
		return echo.NewHTTPError(http.StatusForbidden, "bad signature")
	}
	s.pool.EnqueueFeed(sub.FeedID)
	return c.NoContent(http.StatusOK)
}

func (s *Server) oauthToken(c echo.Context) error {
	if !s.cfg.OAuth.Enabled {
		return echo.NewHTTPError(http.StatusNotFound, "oauth disabled")
	}
	ct := c.Request().Header.Get("Content-Type")
	var grant, clientID, clientSecret string
	if strings.Contains(ct, "application/json") {
		var body map[string]string
		if err := json.NewDecoder(io.LimitReader(c.Request().Body, 1<<20)).Decode(&body); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid json")
		}
		grant = body["grant_type"]
		clientID = body["client_id"]
		clientSecret = body["client_secret"]
	} else {
		_ = c.Request().ParseForm()
		grant = c.FormValue("grant_type")
		clientID = c.FormValue("client_id")
		clientSecret = c.FormValue("client_secret")
	}
	if clientID == "" {
		user, pass, ok := c.Request().BasicAuth()
		if ok {
			clientID, clientSecret = user, pass
		}
	}
	if grant != "client_credentials" {
		return echo.NewHTTPError(http.StatusBadRequest, "unsupported grant_type")
	}
	if clientID == "" || clientSecret == "" {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid_client")
	}
	cl, err := s.st.LookupOAuthClient(c.Request().Context(), clientID)
	if err != nil || cl == nil || cl.RevokedAt != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid_client")
	}
	sum := auth.HashToken(clientSecret)
	if subtle.ConstantTimeCompare([]byte(sum), []byte(cl.SecretHash)) != 1 {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid_client")
	}
	plain, exp, err := s.st.IssueOAuthToken(c.Request().Context(), cl.ClientID, cl.Level, s.cfg.OAuthTTL())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{
		"access_token": plain,
		"token_type":   "Bearer",
		"expires_in":   int(time.Until(exp).Seconds()),
		"scope":        string(cl.Level),
	})
}

type oauthClientReq struct {
	Name  string `json:"name"`
	Level string `json:"level"`
}

func (s *Server) createOAuthClient(c echo.Context) error {
	var req oauthClientReq
	if err := c.Bind(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "name required")
	}
	lv, err := auth.ParseLevel(req.Level)
	if err != nil {
		lv, _ = auth.ParseLevel(s.cfg.OAuth.DefaultLevel)
		if lv == "" {
			lv = auth.LevelStandard
		}
	}
	cid, secret, rec, err := s.st.CreateOAuthClient(c.Request().Context(), req.Name, lv)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, map[string]any{
		"id": rec.ID, "name": rec.Name, "client_id": cid, "client_secret": secret, "level": rec.Level,
	})
}

func (s *Server) listOAuthClients(c echo.Context) error {
	list, err := s.st.ListOAuthClients(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"clients": list, "count": len(list)})
}

func (s *Server) revokeOAuthClient(c echo.Context) error {
	ok, err := s.st.RevokeOAuthClient(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	if !ok {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	return c.JSON(http.StatusOK, map[string]any{"revoked": true})
}

// conditionalGET applies ETag / Last-Modified for catalog GETs. Returns true if 304 written.
func (s *Server) conditionalGET(c echo.Context) (bool, error) {
	rev, err := s.st.CatalogRev(c.Request().Context())
	if err != nil {
		return false, err
	}
	updated, _ := s.st.CatalogUpdatedAt(c.Request().Context())
	etag := `W/"catalog-` + rev + `"`
	c.Response().Header().Set("ETag", etag)
	if updated != "" {
		if t, err := time.Parse(time.RFC3339, updated); err == nil {
			c.Response().Header().Set("Last-Modified", t.UTC().Format(http.TimeFormat))
		}
	}
	inm := c.Request().Header.Get("If-None-Match")
	if inm != "" && (inm == etag || strings.Contains(inm, etag)) {
		return true, c.NoContent(http.StatusNotModified)
	}
	ims := c.Request().Header.Get("If-Modified-Since")
	if ims != "" && updated != "" {
		if imsTime, err := http.ParseTime(ims); err == nil {
			if catTime, err := time.Parse(time.RFC3339, updated); err == nil && !catTime.After(imsTime) {
				return true, c.NoContent(http.StatusNotModified)
			}
		}
	}
	return false, nil
}
