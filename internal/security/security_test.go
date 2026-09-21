package security_test

import (
	"net/http"
	"testing"

	"github.com/Quad4-Software/rss-discovery/internal/security"
)

func TestProbeAndScanner(t *testing.T) {
	if !security.IsProbePath("/.env") {
		t.Fatal(".env")
	}
	if !security.IsProbePath("/wp-admin/index.php") {
		t.Fatal("wp-admin")
	}
	if security.IsProbePath("/v1/feeds") {
		t.Fatal("api path ok")
	}
	if !security.IsScannerUA("sqlmap/1.0") {
		t.Fatal("sqlmap")
	}
	if !security.IsScraperUA("Bytespider") {
		t.Fatal("bytespider")
	}
	if security.IsScannerUA("Quad4Reader/1.0") {
		t.Fatal("legit ua")
	}
	if !security.EmptyOrGenericUA("") {
		t.Fatal("empty ua")
	}
}

func TestMaintenance(t *testing.T) {
	security.SetMaintenance(true)
	if !security.Maintenance() {
		t.Fatal("on")
	}
	security.SetMaintenance(false)
	if security.Maintenance() {
		t.Fatal("off")
	}
}

func TestClientIP(t *testing.T) {
	r, _ := http.NewRequest("GET", "/", nil)
	r.RemoteAddr = "1.2.3.4:9999"
	if security.ClientIP(r, false) != "1.2.3.4" {
		t.Fatal(security.ClientIP(r, false))
	}
	r.Header.Set("X-Forwarded-For", "9.9.9.9, 8.8.8.8")
	if security.ClientIP(r, true) != "9.9.9.9" {
		t.Fatal("xff")
	}
	if security.ClientIP(r, false) != "1.2.3.4" {
		t.Fatal("untrusted xff ignored")
	}
}
