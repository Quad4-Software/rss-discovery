package security_test

import (
	"testing"

	"github.com/Quad4-Software/rss-discovery/internal/security"
)

func BenchmarkIsProbePath(b *testing.B) {
	paths := []string{"/v1/feeds", "/.env", "/wp-admin/x", "/openapi.json", "/solr/admin"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = security.IsProbePath(paths[i%len(paths)])
	}
}

func BenchmarkIsScannerUA(b *testing.B) {
	uas := []string{"Quad4Test/1.0", "sqlmap/1.5", "Feedly/1.0", "nuclei"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = security.IsScannerUA(uas[i%len(uas)])
	}
}
