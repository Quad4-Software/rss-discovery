package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Quad4-Software/rss-discovery/internal/config"
)

func TestDefaultMaxBytes(t *testing.T) {
	cfg := config.Default()
	if cfg.Limits.MaxTotalBytes != 1<<30 {
		t.Fatalf("want 1GiB got %d", cfg.Limits.MaxTotalBytes)
	}
}

func TestLoadTOMLAndEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.toml")
	content := `
data_dir = "` + dir + `"
db_path = "` + filepath.Join(dir, "t.duckdb") + `"
[limits]
max_total_bytes = 1048576
[server]
addr = "127.0.0.1:9999"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RSS_DISCOVERY_WORKERS_FETCH_WORKERS", "3")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Limits.MaxTotalBytes != 1048576 {
		t.Fatalf("max bytes %d", cfg.Limits.MaxTotalBytes)
	}
	if cfg.Server.Addr != "127.0.0.1:9999" {
		t.Fatalf("addr %s", cfg.Server.Addr)
	}
	if cfg.Workers.FetchWorkers != 3 {
		t.Fatalf("workers %d", cfg.Workers.FetchWorkers)
	}
}
