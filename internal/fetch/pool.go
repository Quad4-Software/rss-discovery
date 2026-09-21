package fetch

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/Quad4-Software/rss-discovery/internal/blob"
	"github.com/Quad4-Software/rss-discovery/internal/config"
	"github.com/Quad4-Software/rss-discovery/internal/extract"
	"github.com/Quad4-Software/rss-discovery/internal/httpx"
	"github.com/Quad4-Software/rss-discovery/internal/parse"
	"github.com/Quad4-Software/rss-discovery/internal/security"
	"github.com/Quad4-Software/rss-discovery/internal/store"
	"github.com/Quad4-Software/rss-discovery/internal/webhook"
	"github.com/Quad4-Software/rss-discovery/internal/websub"
)

// Pool runs multi-worker feed and article jobs with crash-safe store writes.
type Pool struct {
	cfg      config.Config
	store    *store.Store
	http     *httpx.Client
	blobs    *blob.Store
	hooks    *webhook.Dispatcher
	websub   *websub.Client
	feedQ    chan string
	artQ     chan artJob
	wg       sync.WaitGroup
	cancel   context.CancelFunc
}

type artJob struct {
	entryID string
	feedID  string
	url     string
}

func NewPool(cfg config.Config, st *store.Store, blobs *blob.Store) *Pool {
	ua := cfg.Workers.UserAgent
	if ua == "" {
		ua = httpx.DefaultUA
	}
	p := &Pool{
		cfg:   cfg,
		store: st,
		http: httpx.NewWithOptions(httpx.Options{
			Timeout:               cfg.HTTPTimeout(),
			MaxIdle:               cfg.Workers.HTTPMaxIdle,
			UserAgent:             ua,
			MaxBytes:              cfg.Workers.MaxFeedBytes,
			ConcurrencyPerHost:    cfg.Workers.ConcurrencyPerHost,
			RequestsPerHostPerMin: cfg.Workers.RequestsPerHostPerMin,
			MinHostGap:            time.Duration(cfg.Workers.MinHostGapMs) * time.Millisecond,
		}),
		blobs: blobs,
		hooks: &webhook.Dispatcher{Store: st, Timeout: 10 * time.Second},
		feedQ: make(chan string, cfg.Workers.QueueSize),
		artQ:  make(chan artJob, cfg.Workers.QueueSize),
	}
	if cfg.WebSub.Enabled && cfg.WebSub.CallbackURL != "" {
		p.websub = &websub.Client{
			Callback:  cfg.WebSub.CallbackURL,
			UserAgent: ua,
			LeaseSecs: cfg.WebSub.LeaseSeconds,
			HTTP: &http.Client{
				Timeout:       20 * time.Second,
				CheckRedirect: httpx.SSRFCheckRedirect,
			},
		}
	}
	return p
}

func (p *Pool) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	p.cancel = cancel
	for i := 0; i < p.cfg.Workers.FetchWorkers; i++ {
		p.wg.Add(1)
		go p.feedWorker(ctx, i)
	}
	for i := 0; i < p.cfg.Workers.ExtractWorkers; i++ {
		p.wg.Add(1)
		go p.articleWorker(ctx, i)
	}
}

func (p *Pool) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
	p.http.CloseIdleConnections()
	if p.websub != nil && p.websub.HTTP != nil {
		p.websub.HTTP.CloseIdleConnections()
	}
}

// EnqueueFeed schedules a feed URL (must already exist in catalog) for refresh.
func (p *Pool) EnqueueFeed(feedID string) bool {
	select {
	case p.feedQ <- feedID:
		return true
	default:
		return false
	}
}

// EnqueueArticle schedules readability extraction for an entry URL.
func (p *Pool) EnqueueArticle(entryID, feedID, pageURL string) bool {
	select {
	case p.artQ <- artJob{entryID: entryID, feedID: feedID, url: pageURL}:
		return true
	default:
		return false
	}
}

func (p *Pool) feedWorker(ctx context.Context, id int) {
	defer p.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case feedID := <-p.feedQ:
			if err := p.refreshFeed(ctx, feedID); err != nil {
				slog.Debug("feed worker", "id", id, "feed", logSafe(feedID), "err", logSafe(err.Error()))
			}
		}
	}
}

func (p *Pool) articleWorker(ctx context.Context, id int) {
	defer p.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-p.artQ:
			if err := p.fetchArticle(ctx, job); err != nil {
				slog.Debug("article worker", "id", id, "entry", logSafe(job.entryID), "err", logSafe(err.Error()))
			}
		}
	}
}

func (p *Pool) refreshFeed(ctx context.Context, feedID string) error {
	f, err := p.store.GetFeed(ctx, feedID)
	if err != nil || f == nil {
		return err
	}
	if err := security.ValidatePublicHTTPSURL(f.URL); err != nil {
		_ = p.store.RecordFetch(ctx, feedID, 0, 0, err.Error(), "", "", false)
		return err
	}
	etag, _ := p.store.GetMeta(ctx, "etag:"+feedID)
	lastMod, _ := p.store.GetMeta(ctx, "lastmod:"+feedID)
	resp, err := p.http.Get(ctx, f.URL, etag, lastMod)
	if err != nil {
		_ = p.store.RecordFetch(ctx, feedID, 0, 0, err.Error(), "", "", false)
		_ = p.store.RecordLatencySample(ctx, feedID, 0, false, "")
		return err
	}
	if resp.Status == 304 {
		_ = p.store.RecordFetch(ctx, feedID, 304, resp.Latency.Milliseconds(), "", resp.ETag, resp.LastModified, true)
		_ = p.store.RecordLatencySample(ctx, feedID, resp.Latency.Milliseconds(), true, f.Format)
		return nil
	}
	if resp.Status < 200 || resp.Status >= 300 {
		_ = p.store.RecordFetch(ctx, feedID, resp.Status, resp.Latency.Milliseconds(), "http error", resp.ETag, resp.LastModified, false)
		_ = p.store.RecordLatencySample(ctx, feedID, resp.Latency.Milliseconds(), false, "")
		return nil
	}
	parsed, err := parse.Feed(resp.Body, feedID)
	if err != nil {
		_ = p.store.RecordFetch(ctx, feedID, resp.Status, resp.Latency.Milliseconds(), err.Error(), resp.ETag, resp.LastModified, false)
		_ = p.store.RecordLatencySample(ctx, feedID, resp.Latency.Milliseconds(), false, "")
		return err
	}
	meta := store.Feed{
		ID:          feedID,
		URL:         f.URL,
		SiteURL:     first(parsed.Link, f.SiteURL),
		Title:       first(parsed.Title, f.Title),
		Description: first(parsed.Description, f.Description),
		Format:      parsed.Format,
		Language:    parsed.Language,
		FaviconURL:  f.FaviconURL,
		Category:    f.Category,
		Blurb:       f.Blurb,
		Source:      f.Source,
	}
	if meta.FaviconURL == "" && meta.SiteURL != "" {
		meta.FaviconURL = guessFavicon(meta.SiteURL)
	}
	_ = p.store.UpsertFeed(ctx, meta)
	_ = p.store.ReplaceEntries(ctx, feedID, parsed.Entries, p.cfg.Limits.MaxEntriesPerFeed)
	_ = p.store.RecordFetch(ctx, feedID, resp.Status, resp.Latency.Milliseconds(), "", resp.ETag, resp.LastModified, true)
	_ = p.store.RecordLatencySample(ctx, feedID, resp.Latency.Milliseconds(), true, meta.Format)
	_ = p.store.RecomputeFeedEmbedding(ctx, meta)
	_ = p.store.BumpCatalogRev(ctx)
	if resp.ETag != "" {
		_ = p.store.SetMeta(ctx, "etag:"+feedID, resp.ETag)
	}
	if resp.LastModified != "" {
		_ = p.store.SetMeta(ctx, "lastmod:"+feedID, resp.LastModified)
	}
	if meta.FaviconURL != "" {
		go p.fetchFavicon(context.Background(), feedID, meta.FaviconURL)
	}
	p.maybeSubscribeWebSub(ctx, feedID, f.URL, resp)
	if p.hooks != nil {
		p.hooks.NotifyFeedUpdated(ctx, meta)
	}
	return nil
}

func (p *Pool) maybeSubscribeWebSub(ctx context.Context, feedID, feedURL string, resp *httpx.Response) {
	if p.websub == nil || !p.cfg.WebSub.Enabled {
		return
	}
	h := http.Header{}
	if resp.Link != "" {
		h.Add("Link", resp.Link)
	}
	disc := websub.Merge(websub.DiscoverFromHeaders(h), websub.DiscoverFromBody(resp.Body))
	if disc.Hub == "" {
		return
	}
	if err := security.ValidatePublicHTTPSURL(disc.Hub); err != nil {
		return
	}
	topic := disc.Self
	if topic == "" {
		topic = feedURL
	}
	if err := security.ValidatePublicHTTPSURL(topic); err != nil {
		return
	}

	existing, _ := p.store.GetWebSubByFeed(ctx, feedID)
	secret := ""
	needSubscribe := true
	if existing != nil {
		secret = existing.Secret
		sameHub := existing.Hub == disc.Hub && existing.Topic == topic
		if sameHub && (existing.Status == store.WebSubActive || existing.Status == store.WebSubPending) {
			if existing.ExpiresAt != nil && time.Until(*existing.ExpiresAt) > time.Hour {
				needSubscribe = false
			}
		}
		if secret == "" {
			secret = websub.NewSecret()
		}
	} else {
		secret = websub.NewSecret()
	}
	if !needSubscribe {
		return
	}
	sub := store.WebSubSub{
		FeedID: feedID, Topic: topic, Hub: disc.Hub, Secret: secret,
		LeaseSecs: p.cfg.WebSub.LeaseSeconds, Status: store.WebSubPending,
	}
	if existing != nil {
		sub.ID = existing.ID
	}
	if err := p.store.UpsertWebSub(ctx, sub); err != nil {
		return
	}
	go func() {
		err := p.websub.Subscribe(context.Background(), disc.Hub, topic, secret)
		if err != nil {
			sub.Status = store.WebSubDenied
			sub.LastError = err.Error()
			_ = p.store.UpsertWebSub(context.Background(), sub)
			return
		}
	}()
}

func (p *Pool) fetchArticle(ctx context.Context, job artJob) error {
	if err := security.ValidatePublicHTTPSURL(job.url); err != nil {
		return err
	}
	existing, _ := p.store.GetFullText(ctx, job.entryID)
	if existing != nil && existing.Text != "" {
		return nil
	}
	maxBytes := p.cfg.Workers.MaxArticleBytes
	if maxBytes < 1 {
		maxBytes = 2 << 20
	}
	client := httpx.NewWithOptions(httpx.Options{
		Timeout:               p.cfg.HTTPTimeout(),
		MaxIdle:               8,
		UserAgent:             p.cfg.Workers.UserAgent,
		MaxBytes:              maxBytes,
		ConcurrencyPerHost:    p.cfg.Workers.ConcurrencyPerHost,
		RequestsPerHostPerMin: p.cfg.Workers.RequestsPerHostPerMin,
		MinHostGap:            time.Duration(p.cfg.Workers.MinHostGapMs) * time.Millisecond,
	})
	defer client.CloseIdleConnections()
	resp, err := client.Get(ctx, job.url, "", "")
	if err != nil {
		return err
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return fmt.Errorf("article http %d", resp.Status)
	}
	art, err := extract.FromHTML(string(resp.Body), job.url)
	if err != nil {
		return err
	}
	key := path.Join("fulltext", job.feedID, job.entryID+".txt")
	storage := "local"
	text := art.Text
	if p.blobs != nil && len(text) > 16*1024 {
		st, err := p.blobs.Put(ctx, key, []byte(text))
		if err == nil {
			storage = st
			// Keep excerpt inline when body lives in blob store.
			text = art.Excerpt
		}
	}
	ft := store.FullText{
		EntryID:  job.entryID,
		FeedID:   job.feedID,
		URL:      job.url,
		Title:    art.Title,
		Text:     text,
		Excerpt:  art.Excerpt,
		Byline:   art.Byline,
		SiteName: art.SiteName,
		ImageURL: art.ImageURL,
		Storage:  storage,
		BlobKey:  key,
		ByteSize: art.Length,
	}
	return p.store.UpsertFullText(ctx, ft)
}

func (p *Pool) fetchFavicon(ctx context.Context, feedID, favURL string) {
	if err := security.ValidatePublicHTTPSURL(favURL); err != nil {
		return
	}
	client := httpx.NewWithOptions(httpx.Options{
		Timeout:               10 * time.Second,
		MaxIdle:               8,
		UserAgent:             p.cfg.Workers.UserAgent,
		MaxBytes:              256 << 10,
		ConcurrencyPerHost:    2,
		RequestsPerHostPerMin: p.cfg.Workers.RequestsPerHostPerMin,
		MinHostGap:            time.Duration(p.cfg.Workers.MinHostGapMs) * time.Millisecond,
	})
	defer client.CloseIdleConnections()
	resp, err := client.Get(ctx, favURL, "", "")
	if err != nil || resp.Status >= 400 {
		return
	}
	_ = p.store.SaveFavicon(ctx, feedID, favURL, resp.ContentType, resp.Body)
}

// RefreshFeed synchronously refreshes one feed (API use).
func (p *Pool) RefreshFeed(ctx context.Context, feedID string) error {
	return p.refreshFeed(ctx, feedID)
}

// FetchArticle synchronously extracts one article.
func (p *Pool) FetchArticle(ctx context.Context, entryID, feedID, pageURL string) error {
	return p.fetchArticle(ctx, artJob{entryID: entryID, feedID: feedID, url: pageURL})
}

// logSafe strips CR and LF so untrusted values cannot forge log entries.
func logSafe(s string) string {
	s = strings.ReplaceAll(s, "\n", "")
	return strings.ReplaceAll(s, "\r", "")
}

func first(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func guessFavicon(site string) string {
	u, err := url.Parse(site)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	u.Path = "/favicon.ico"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
