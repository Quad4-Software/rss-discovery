package security_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Quad4-Software/rss-discovery/internal/security"
)

func TestBlocklistFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bl.txt")
	content := "# comment\npath:/evil\nua:evilbot\ncidr:203.0.113.0/24\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	bl, err := security.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	security.SetGlobal(bl)
	t.Cleanup(func() { security.SetGlobal(nil) })
	if !bl.MatchPath("/evil/x") {
		t.Fatal("path")
	}
	if !bl.MatchUA("Mozilla evilbot/1") {
		t.Fatal("ua")
	}
	if !bl.MatchIP("203.0.113.50") {
		t.Fatal("cidr")
	}
	if bl.MatchIP("8.8.8.8") {
		t.Fatal("public ok")
	}
}

func TestLoadRemoteRejectsHTTP(t *testing.T) {
	_, err := security.LoadRemote(t.Context(), "http://example.com/bl.txt", 1024, 0)
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("want https error got %v", err)
	}
}

func TestValidatePublicURL(t *testing.T) {
	cases := []struct {
		url string
		ok  bool
	}{
		{"https://example.com/feed.xml", true},
		{"http://example.com/rss", true},
		{"javascript:alert(1)", false},
		{"file:///etc/passwd", false},
		{"http://127.0.0.1/feed", false},
		{"http://localhost/feed", false},
		{"http://192.168.1.1/x", false},
		{"https://user:pass@example.com/x", false},
	}
	for _, tc := range cases {
		err := security.ValidatePublicHTTPSURL(tc.url)
		if tc.ok && err != nil {
			t.Fatalf("%s: %v", tc.url, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("%s: expected error", tc.url)
		}
	}
}

func TestExpandedProbePaths(t *testing.T) {
	for _, p := range []string{
		"/.aws/credentials", "/owa/auth/logon.aspx", "/_ignition/execute-solution", "/solr/admin",
		"/actuator/heapdump", "/swagger-ui.html", "/package.json", "/_cluster/health", "/nacos/v1/auth/users",
	} {
		if !security.IsProbePath(p) {
			t.Fatalf("missing probe %s", p)
		}
	}
	if security.IsProbePath("/openapi.json") || security.IsProbePath("/docs") || security.IsProbePath("/v1/feeds") {
		t.Fatal("legit paths must not be probes")
	}
}
