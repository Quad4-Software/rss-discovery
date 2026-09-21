package security_test

import (
	"net"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Quad4-Software/rss-discovery/internal/security"
)

func FuzzValidatePublicHTTPSURL(f *testing.F) {
	seeds := []string{
		"https://example.com/feed.xml",
		"http://example.com/rss",
		"javascript:alert(1)",
		"file:///etc/passwd",
		"http://127.0.0.1/feed",
		"http://localhost/x",
		"http://[::1]/",
		"http://192.168.0.1/a",
		"http://10.0.0.1/a",
		"http://172.16.0.1/a",
		"https://user:pass@example.com/x",
		"https://example.com/" + strings.Repeat("a", 4096),
		"ftp://example.com/x",
		"https://169.254.169.254/latest/meta-data/",
		"",
		"https://",
		"https:///path",
		"http://example.local/feed",
		"http://svc.internal/feed",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if !utf8.ValidString(raw) {
			return
		}
		err := security.ValidatePublicHTTPSURL(raw)
		lower := strings.ToLower(raw)
		if strings.HasPrefix(lower, "javascript:") || strings.HasPrefix(lower, "file:") ||
			strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "ftp:") {
			if err == nil {
				t.Fatalf("dangerous scheme accepted: %q", raw)
			}
		}
		if strings.Contains(lower, "127.0.0.1") || strings.Contains(lower, "localhost") ||
			strings.Contains(lower, "[::1]") || strings.Contains(lower, "0.0.0.0") {
			if err == nil {
				t.Fatalf("loopback accepted: %q", raw)
			}
		}
		if strings.Contains(lower, "user:pass@") {
			if err == nil {
				t.Fatalf("credentials accepted: %q", raw)
			}
		}
	})
}

func FuzzIsProbePath(f *testing.F) {
	for _, s := range []string{"/.env", "/v1/feeds", "/wp-admin", "/openapi.json", "/docs", "/../../../etc/passwd", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, path string) {
		if !utf8.ValidString(path) {
			return
		}
		hit := security.IsProbePath(path)
		switch path {
		case "/v1/feeds", "/openapi.json", "/docs", "/healthz":
			if hit {
				t.Fatalf("legit path flagged: %q", path)
			}
		case "/.env", "/wp-admin":
			if !hit {
				t.Fatalf("probe missed: %q", path)
			}
		}
	})
}

func FuzzIsScannerUA(f *testing.F) {
	for _, s := range []string{"Quad4Test/1.0", "sqlmap/1.5", "nuclei", "Mozilla/5.0", "", "ffuf", "Feedly/1.0"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, ua string) {
		if !utf8.ValidString(ua) {
			return
		}
		_ = security.IsScannerUA(ua)
		_ = security.IsScraperUA(ua)
		_ = security.EmptyOrGenericUA(ua)
		_ = security.IsBlocklistedUA(ua)
	})
}

func FuzzIsPublicIP(f *testing.F) {
	for _, s := range []string{"8.8.8.8", "127.0.0.1", "::1", "10.0.0.1", "192.168.1.1", "169.254.169.254", "1.1.1.1", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !utf8.ValidString(s) {
			return
		}
		ip := net.ParseIP(s)
		if ip == nil {
			return
		}
		pub := security.IsPublicIP(ip)
		switch s {
		case "127.0.0.1", "::1", "10.0.0.1", "192.168.1.1", "169.254.169.254":
			if pub {
				t.Fatalf("private/metadata IP marked public: %s", s)
			}
		case "8.8.8.8", "1.1.1.1":
			if !pub {
				t.Fatalf("public IP marked private: %s", s)
			}
		}
	})
}
