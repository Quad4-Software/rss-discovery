package security

import (
	"net"
	"net/http"
	"strings"
	"sync/atomic"
)

var maintenance atomic.Bool

// SetMaintenance toggles global maintenance mode.
func SetMaintenance(on bool) { maintenance.Store(on) }

// Maintenance returns whether the service is in maintenance mode.
func Maintenance() bool { return maintenance.Load() }

// probePaths is populated in paths.go init and may grow via AddProbePaths / blocklists.
var probePaths []string

var scannerUA = []string{
	"sqlmap", "nikto", "nmap", "masscan", "zgrab", "dirbuster", "gobuster",
	"wpscan", "nuclei", "fuzz", "acunetix", "nessus", "openvas", "w3af",
	"havij", "pangolin", "comsenz", "zmeu", "httpx", "feroxbuster",
	"ffuf", "wfuzz", "whatweb", "jaeles", "xray", "katana",
}

var scraperUA = []string{
	"bytespider", "gptbot", "ccbot", "semrush", "ahrefs", "mj12bot",
	"petalbot", "dotbot", "dataforseo", "claudebot", "amazonbot",
	"scrapy", "httrack", "heritrix", "bingbot", "yandexbot",
	"meta-externalagent", "facebookexternalhit", "applebot",
}

// IsScannerUA flags vulnerability scanners.
func IsScannerUA(ua string) bool {
	ua = strings.ToLower(strings.TrimSpace(ua))
	if ua == "" {
		return false
	}
	for _, bad := range scannerUA {
		if strings.Contains(ua, bad) {
			return true
		}
	}
	return false
}

// IsBlocklistedUA reports custom blocklist UA substrings.
func IsBlocklistedUA(ua string) bool {
	return Global().MatchUA(ua)
}

// IsScraperUA flags known archival/AI scrapers.
func IsScraperUA(ua string) bool {
	ua = strings.ToLower(strings.TrimSpace(ua))
	for _, bad := range scraperUA {
		if strings.Contains(ua, bad) {
			return true
		}
	}
	return false
}

// EmptyOrGenericUA is true for blank or placeholder browser strings.
func EmptyOrGenericUA(ua string) bool {
	ua = strings.ToLower(strings.TrimSpace(ua))
	return ua == "" || ua == "-" || ua == "*" || ua == "mozilla/4.0" || ua == "mozilla/5.0"
}

// ClientIP extracts a best-effort client IP without trusting spoofable headers
// unless behind a configured trusted proxy. With trustProxy the rightmost
// X-Forwarded-For entry is used: the proxy appended it from the actual peer
// address, while every earlier entry is client-supplied and spoofable.
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
