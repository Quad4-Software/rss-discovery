package security

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ValidatePublicHTTPSURL rejects non-http(s), credentials-in-URL, and private/literal targets (SSRF).
func ValidatePublicHTTPSURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("url required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("url scheme must be http or https")
	}
	if u.User != nil {
		return fmt.Errorf("url credentials not allowed")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("url host required")
	}
	if host == "localhost" || strings.HasSuffix(strings.ToLower(host), ".local") || strings.HasSuffix(strings.ToLower(host), ".internal") {
		return fmt.Errorf("blocked host")
	}
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIP(ip) {
			return fmt.Errorf("blocked ip")
		}
		return nil
	}
	// DNS check for private resolution. Fail closed on lookup errors for user-controlled URLs.
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("host lookup failed")
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return fmt.Errorf("blocked private resolution for %s", host)
		}
	}
	return nil
}

// IsPublicIP reports whether ip is safe for outbound server-side fetches.
func IsPublicIP(ip net.IP) bool {
	return isPublicIP(ip)
}

func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	// Carrier-grade NAT / documentation ranges
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return false
		}
		if ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 0 {
			return false
		}
		if ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 2 {
			return false
		}
		if ip4[0] == 198 && (ip4[1] == 18 || ip4[1] == 19) {
			return false
		}
		if ip4[0] == 198 && ip4[1] == 51 && ip4[2] == 100 {
			return false
		}
		if ip4[0] == 203 && ip4[1] == 0 && ip4[2] == 113 {
			return false
		}
	}
	return true
}
