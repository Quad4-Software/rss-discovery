package websub

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Quad4-Software/rss-discovery/internal/httpx"
	"github.com/Quad4-Software/rss-discovery/internal/security"
)

// Discovery holds hub and self URLs from Link headers or HTML/XML body.
type Discovery struct {
	Hub  string
	Self string
}

var linkRe = regexp.MustCompile(`(?i)<([^>]+)>\s*;\s*rel=(?:"|')?(hub|self)(?:"|')?`)

// DiscoverFromHeaders parses Link response headers.
func DiscoverFromHeaders(h http.Header) Discovery {
	var d Discovery
	for _, v := range h.Values("Link") {
		for _, part := range strings.Split(v, ",") {
			m := linkRe.FindStringSubmatch(strings.TrimSpace(part))
			if len(m) != 3 {
				continue
			}
			switch strings.ToLower(m[2]) {
			case "hub":
				if d.Hub == "" {
					d.Hub = m[1]
				}
			case "self":
				if d.Self == "" {
					d.Self = m[1]
				}
			}
		}
	}
	return d
}

// DiscoverFromBody finds atom:link rel=hub/self in feed XML.
func DiscoverFromBody(body []byte) Discovery {
	var d Discovery
	s := string(body)
	// Cheap scans for common patterns.
	hubRe := regexp.MustCompile(`(?i)rel=["']hub["'][^>]*href=["']([^"']+)["']|href=["']([^"']+)["'][^>]*rel=["']hub["']`)
	selfRe := regexp.MustCompile(`(?i)rel=["']self["'][^>]*href=["']([^"']+)["']|href=["']([^"']+)["'][^>]*rel=["']self["']`)
	if m := hubRe.FindStringSubmatch(s); len(m) > 0 {
		d.Hub = firstGroup(m)
	}
	if m := selfRe.FindStringSubmatch(s); len(m) > 0 {
		d.Self = firstGroup(m)
	}
	return d
}

func firstGroup(m []string) string {
	for i := 1; i < len(m); i++ {
		if m[i] != "" {
			return m[i]
		}
	}
	return ""
}

// Merge prefers header discovery then body.
func Merge(a, b Discovery) Discovery {
	if a.Hub == "" {
		a.Hub = b.Hub
	}
	if a.Self == "" {
		a.Self = b.Self
	}
	return a
}

// Client talks to WebSub hubs.
type Client struct {
	HTTP       *http.Client
	Callback   string
	UserAgent  string
	LeaseSecs  int
}

// NewSecret returns a random hex secret.
func NewSecret() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Subscribe sends a hub.mode=subscribe request.
func (c *Client) Subscribe(ctx context.Context, hub, topic, secret string) error {
	return c.mode(ctx, hub, topic, secret, "subscribe")
}

// Unsubscribe sends hub.mode=unsubscribe.
func (c *Client) Unsubscribe(ctx context.Context, hub, topic, secret string) error {
	return c.mode(ctx, hub, topic, secret, "unsubscribe")
}

func (c *Client) mode(ctx context.Context, hub, topic, secret, mode string) error {
	if err := security.ValidatePublicHTTPSURL(hub); err != nil {
		return fmt.Errorf("hub url: %w", err)
	}
	if err := security.ValidatePublicHTTPSURL(topic); err != nil {
		return fmt.Errorf("topic url: %w", err)
	}
	cli := c.HTTP
	if cli == nil {
		cli = &http.Client{
			Timeout:       20 * time.Second,
			CheckRedirect: httpx.SSRFCheckRedirect,
		}
	}
	lease := c.LeaseSecs
	if lease < 3600 {
		lease = 86400
	}
	form := url.Values{}
	form.Set("hub.mode", mode)
	form.Set("hub.topic", topic)
	form.Set("hub.callback", c.Callback)
	form.Set("hub.lease_seconds", strconv.Itoa(lease))
	if secret != "" {
		form.Set("hub.secret", secret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hub, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ua := c.UserAgent
	if ua == "" {
		ua = "rss-discovery-websub/1.0"
	}
	req.Header.Set("User-Agent", ua)
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("hub %s returned %d", mode, resp.StatusCode)
	}
	return nil
}

// VerifySignature checks X-Hub-Signature (sha1= or sha256=).
func VerifySignature(secret string, body []byte, header string) bool {
	header = strings.TrimSpace(header)
	if secret == "" || header == "" {
		return false
	}
	lower := strings.ToLower(header)
	var sum []byte
	switch {
	case strings.HasPrefix(lower, "sha256="):
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(body)
		sum = mac.Sum(nil)
		want, err := hex.DecodeString(header[len("sha256="):])
		if err != nil {
			return false
		}
		return hmac.Equal(sum, want)
	case strings.HasPrefix(lower, "sha1="):
		mac := hmac.New(sha1.New, []byte(secret))
		_, _ = mac.Write(body)
		sum = mac.Sum(nil)
		want, err := hex.DecodeString(header[len("sha1="):])
		if err != nil {
			return false
		}
		return hmac.Equal(sum, want)
	default:
		return false
	}
}
