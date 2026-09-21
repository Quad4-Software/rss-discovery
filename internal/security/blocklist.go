package security

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Blocklist holds path prefixes, UA substrings, and CIDR bans loaded from local/remote sources.
type Blocklist struct {
	mu        sync.RWMutex
	paths     []string
	uas       []string
	cidrs     []*net.IPNet
	updatedAt time.Time
	source    string
}

var globalBL atomic.Pointer[Blocklist]

func init() {
	globalBL.Store(&Blocklist{})
}

// Global returns the process blocklist.
func Global() *Blocklist { return globalBL.Load() }

// SetGlobal replaces the active blocklist.
func SetGlobal(b *Blocklist) {
	if b == nil {
		b = &Blocklist{}
	}
	globalBL.Store(b)
}

// MatchPath reports whether path hits an extra blocklist prefix.
func (b *Blocklist) MatchPath(path string) bool {
	if b == nil {
		return false
	}
	p := strings.ToLower(path)
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, pref := range b.paths {
		if strings.HasPrefix(p, pref) {
			return true
		}
	}
	return false
}

// MatchUA reports whether ua hits an extra blocklist substring.
func (b *Blocklist) MatchUA(ua string) bool {
	if b == nil {
		return false
	}
	ua = strings.ToLower(ua)
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, s := range b.uas {
		if strings.Contains(ua, s) {
			return true
		}
	}
	return false
}

// MatchIP reports whether ip is in a banned CIDR.
func (b *Blocklist) MatchIP(ipStr string) bool {
	if b == nil {
		return false
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, c := range b.cidrs {
		if c.Contains(ip) {
			return true
		}
	}
	return false
}

// LoadFile parses a blocklist file.
// Lines: path:<prefix> | ua:<substr> | cidr:<cidr> | #<comment>
func LoadFile(path string) (*Blocklist, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseBlocklist(f, path)
}

// LoadRemote fetches a blocklist over HTTPS with SSRF protections and size caps.
func LoadRemote(ctx context.Context, rawURL string, maxBytes int64, timeout time.Duration) (*Blocklist, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("remote blocklist requires https")
	}
	if err := assertPublicHost(u.Hostname()); err != nil {
		return nil, err
	}
	if maxBytes < 1 {
		maxBytes = 1 << 20
	}
	if timeout < time.Second {
		timeout = 15 * time.Second
	}
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("redirect not https")
			}
			return assertPublicHost(req.URL.Hostname())
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "rss-discovery-blocklist/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("blocklist http %d", resp.StatusCode)
	}
	return parseBlocklist(io.LimitReader(resp.Body, maxBytes), rawURL)
}

func assertPublicHost(host string) error {
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".local") {
		return fmt.Errorf("blocked host")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return err
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("blocked private/literal ip for %s", host)
		}
	}
	return nil
}

func parseBlocklist(r io.Reader, source string) (*Blocklist, error) {
	bl := &Blocklist{source: source, updatedAt: time.Now().UTC()}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		if val == "" {
			continue
		}
		switch key {
		case "path":
			if !strings.HasPrefix(val, "/") {
				val = "/" + val
			}
			bl.paths = append(bl.paths, strings.ToLower(val))
		case "ua":
			bl.uas = append(bl.uas, strings.ToLower(val))
		case "cidr":
			_, n, err := net.ParseCIDR(val)
			if err != nil {
				slog.Warn("blocklist bad cidr", "cidr", val, "err", err)
				continue
			}
			bl.cidrs = append(bl.cidrs, n)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return bl, nil
}

// Merge combines multiple blocklists.
func Merge(lists ...*Blocklist) *Blocklist {
	out := &Blocklist{updatedAt: time.Now().UTC(), source: "merged"}
	for _, b := range lists {
		if b == nil {
			continue
		}
		b.mu.RLock()
		out.paths = append(out.paths, b.paths...)
		out.uas = append(out.uas, b.uas...)
		out.cidrs = append(out.cidrs, b.cidrs...)
		b.mu.RUnlock()
	}
	return out
}

// StartReloader periodically reloads local and optional remote blocklists.
func StartReloader(ctx context.Context, localPath, remoteURL string, every time.Duration) {
	if every < time.Minute {
		every = 15 * time.Minute
	}
	load := func() {
		var parts []*Blocklist
		if localPath != "" {
			if b, err := LoadFile(localPath); err == nil {
				parts = append(parts, b)
			} else if !os.IsNotExist(err) {
				slog.Warn("blocklist local", "err", err)
			}
		}
		if remoteURL != "" {
			cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			b, err := LoadRemote(cctx, remoteURL, 1<<20, 15*time.Second)
			cancel()
			if err != nil {
				slog.Warn("blocklist remote", "err", err)
			} else {
				parts = append(parts, b)
			}
		}
		if len(parts) > 0 {
			SetGlobal(Merge(parts...))
			slog.Info("blocklist loaded", "sources", len(parts))
		}
	}
	load()
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				load()
			}
		}
	}()
}
