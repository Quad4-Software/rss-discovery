package httpx

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/Quad4-Software/rss-discovery/internal/security"
)

var (
	bufPool = sync.Pool{
		New: func() any {
			b := make([]byte, 0, 64*1024)
			return &b
		},
	}
	readBufPool = sync.Pool{
		New: func() any {
			b := make([]byte, 32*1024)
			return &b
		},
	}
)

// DefaultUA follows documented aggregator conventions (FeedFetcher-Google, Feedly, Inoreader,
// Miniflux) while identifying this service. Publishers expect a contact URL in parentheses.
const DefaultUA = "Mozilla/5.0 (compatible; rss-discovery/1.0; +https://github.com/Quad4-Software/rss-discovery)"


// Options configures the polite HTTP client.
type Options struct {
	Timeout            time.Duration
	MaxIdle            int
	UserAgent          string
	MaxBytes           int64
	ConcurrencyPerHost int
	// RequestsPerHostPerMin caps outbound GETs per upstream host.
	RequestsPerHostPerMin int
	// MinHostGap is an additional floor between requests to the same host.
	MinHostGap time.Duration
	// AllowPrivate disables dial-time SSRF checks (tests with loopback only).
	AllowPrivate bool
}

// Client is a connection-efficient HTTP client with per-host concurrency and rate limits.
type Client struct {
	http      *http.Client
	ua        string
	maxBytes  int64
	hostSemas sync.Map // host -> chan struct{}
	hostRate  sync.Map // host -> *hostLimiter
	perHost   int
	perMin    int
	minGap    time.Duration
}

type hostLimiter struct {
	lim  *rate.Limiter
	mu   sync.Mutex
	last time.Time
}

func New(timeout time.Duration, maxIdle int, ua string, maxBytes int64, perHost int) *Client {
	return NewWithOptions(Options{
		Timeout:               timeout,
		MaxIdle:               maxIdle,
		UserAgent:             ua,
		MaxBytes:              maxBytes,
		ConcurrencyPerHost:    perHost,
		RequestsPerHostPerMin: 30,
		MinHostGap:            200 * time.Millisecond,
	})
}

func NewWithOptions(opts Options) *Client {
	if opts.MaxIdle < 8 {
		opts.MaxIdle = 32
	}
	if opts.ConcurrencyPerHost < 1 {
		opts.ConcurrencyPerHost = 2
	}
	if opts.UserAgent == "" {
		opts.UserAgent = DefaultUA
	}
	if opts.RequestsPerHostPerMin < 1 {
		opts.RequestsPerHostPerMin = 30
	}
	if opts.MinHostGap < 0 {
		opts.MinHostGap = 200 * time.Millisecond
	}
	if opts.MaxBytes < 1 {
		opts.MaxBytes = 5 << 20
	}
	baseDialer := &net.Dialer{
		Timeout:   8 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	dial := baseDialer.DialContext
	if !opts.AllowPrivate {
		dial = ssrfDialContext(baseDialer)
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dial,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          opts.MaxIdle,
		MaxIdleConnsPerHost:   opts.ConcurrencyPerHost + 2,
		MaxConnsPerHost:       opts.ConcurrencyPerHost * 2,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   8 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: opts.Timeout,
		DisableCompression:    false,
	}
	return &Client{
		http: &http.Client{
			Timeout:       opts.Timeout,
			Transport:     transport,
			CheckRedirect: SSRFCheckRedirect,
		},
		ua:       opts.UserAgent,
		maxBytes: opts.MaxBytes,
		perHost:  opts.ConcurrencyPerHost,
		perMin:   opts.RequestsPerHostPerMin,
		minGap:   opts.MinHostGap,
	}
}

// CloseIdleConnections releases pooled keep-alive connections on the transport.
func (c *Client) CloseIdleConnections() {
	c.http.CloseIdleConnections()
}

// SSRFCheckRedirect blocks redirects to non-public http(s) targets.
func SSRFCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return fmt.Errorf("too many redirects")
	}
	if req.URL == nil {
		return fmt.Errorf("redirect missing url")
	}
	if err := security.ValidatePublicHTTPSURL(req.URL.String()); err != nil {
		return fmt.Errorf("redirect blocked: %w", err)
	}
	return nil
}

func ssrfDialContext(d *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if ip := net.ParseIP(host); ip != nil {
			if !security.IsPublicIP(ip) {
				return nil, fmt.Errorf("blocked ip")
			}
			return d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("host lookup failed")
		}
		var last error
		for _, ipa := range ips {
			if !security.IsPublicIP(ipa.IP) {
				last = fmt.Errorf("blocked private resolution")
				continue
			}
			c, err := d.DialContext(ctx, network, net.JoinHostPort(ipa.IP.String(), port))
			if err == nil {
				return c, nil
			}
			last = err
		}
		if last == nil {
			last = fmt.Errorf("no public address for host")
		}
		return nil, last
	}
}

type Response struct {
	Status       int
	Body         []byte
	ETag         string
	LastModified string
	ContentType  string
	Link         string
	Latency      time.Duration
	NotModified  bool
}

func (c *Client) hostSem(host string) chan struct{} {
	v, _ := c.hostSemas.LoadOrStore(host, make(chan struct{}, c.perHost))
	return v.(chan struct{})
}

func (c *Client) limiter(host string) *hostLimiter {
	v, _ := c.hostRate.LoadOrStore(host, &hostLimiter{
		lim: rate.NewLimiter(rate.Limit(float64(c.perMin)/60.0), max(1, c.perMin/10)),
	})
	return v.(*hostLimiter)
}

// Get fetches a URL with optional conditional headers, per-host rate limiting, and 429 backoff.
func (c *Client) Get(ctx context.Context, rawURL, etag, lastMod string) (*Response, error) {
	return c.getOnce(ctx, rawURL, etag, lastMod, true)
}

func (c *Client) getOnce(ctx context.Context, rawURL, etag, lastMod string, allowRetry bool) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "application/feed+json, application/atom+xml, application/rss+xml, application/xml, text/xml, */*;q=0.8")
	req.Header.Set("Accept-Encoding", "gzip")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastMod != "" {
		req.Header.Set("If-Modified-Since", lastMod)
	}

	host := req.URL.Host
	sem := c.hostSem(host)
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	hl := c.limiter(host)
	if err := hl.lim.Wait(ctx); err != nil {
		return nil, err
	}
	hl.mu.Lock()
	if !hl.last.IsZero() {
		if wait := c.minGap - time.Since(hl.last); wait > 0 {
			hl.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			hl.mu.Lock()
		}
	}
	hl.last = time.Now()
	hl.mu.Unlock()

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests && allowRetry {
		sleep := retryAfter(resp.Header.Get("Retry-After"), 5*time.Second)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(sleep):
		}
		return c.getOnce(ctx, rawURL, etag, lastMod, false)
	}

	bufPtr := bufPool.Get().(*[]byte)
	buf := (*bufPtr)[:0]
	tmpPtr := readBufPool.Get().(*[]byte)
	tmp := *tmpPtr
	limited := io.LimitReader(resp.Body, c.maxBytes+1)
	for {
		n, rerr := limited.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			readBufPool.Put(tmpPtr)
			bufPool.Put(bufPtr)
			return nil, rerr
		}
	}
	readBufPool.Put(tmpPtr)
	if int64(len(buf)) > c.maxBytes {
		bufPool.Put(bufPtr)
		return nil, fmt.Errorf("body exceeds %d bytes", c.maxBytes)
	}
	out := make([]byte, len(buf))
	copy(out, buf)
	*bufPtr = buf[:0]
	bufPool.Put(bufPtr)

	return &Response{
		Status:       resp.StatusCode,
		Body:         out,
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
		ContentType:  resp.Header.Get("Content-Type"),
		Link:         strings.Join(resp.Header.Values("Link"), ", "),
		Latency:      time.Since(start),
		NotModified:  resp.StatusCode == http.StatusNotModified,
	}, nil
}

func retryAfter(h string, fallback time.Duration) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return fallback
	}
	if secs, err := strconv.Atoi(h); err == nil {
		if secs <= 0 {
			return 0
		}
		d := time.Duration(secs) * time.Second
		if d > 60*time.Second {
			d = 60 * time.Second
		}
		return d
	}
	if t, err := http.ParseTime(h); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		if d > 60*time.Second {
			return 60 * time.Second
		}
		return d
	}
	return fallback
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
