package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Quad4-Software/rss-discovery/internal/auth"
	"github.com/Quad4-Software/rss-discovery/internal/config"
	"github.com/Quad4-Software/rss-discovery/internal/store"
)

func runToken(args []string) int {
	if len(args) < 1 {
		printHelp()
		return 2
	}
	cmd := args[0]
	rest := args[1:]

	configPath := "config.toml"
	// peel --config if present
	filtered := make([]string, 0, len(rest))
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--config" && i+1 < len(rest) {
			configPath = rest[i+1]
			i++
			continue
		}
		if stringsHasPrefix(rest[i], "--config=") {
			configPath = stringsTrimPrefix(rest[i], "--config=")
			continue
		}
		filtered = append(filtered, rest[i])
	}
	rest = filtered

	cfg := config.Default()
	if _, err := os.Stat(configPath); err == nil {
		loaded, err := config.Load(configPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		cfg = loaded
	}

	st, err := openStore(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer st.Close()
	ctx := context.Background()

	switch cmd {
	case "generate":
		fs := flag.NewFlagSet("token generate", flag.ExitOnError)
		name := fs.String("name", "", "token name")
		level := fs.String("level", "standard", "readonly|standard|priority|admin")
		ttl := fs.Duration("ttl", 0, "optional expiry duration")
		_ = fs.Parse(rest)
		if *name == "" {
			fmt.Fprintln(os.Stderr, "--name required")
			return 2
		}
		lv, err := auth.ParseLevel(*level)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		plain, rec, err := auth.Generate(lv, *name, *ttl)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := st.CreateToken(ctx, rec); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("id=%s level=%s name=%s\n", rec.ID, rec.Level, rec.Name)
		fmt.Printf("token=%s\n", plain)
		fmt.Fprintln(os.Stderr, "store the token now it cannot be shown again")
		return 0
	case "revoke":
		if len(rest) < 1 {
			fmt.Fprintln(os.Stderr, "token id required")
			return 2
		}
		ok, err := st.RevokeToken(ctx, rest[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !ok {
			fmt.Fprintln(os.Stderr, "not found or already revoked")
			return 1
		}
		fmt.Println("revoked", rest[0])
		return 0
	case "list":
		_ = st.Close()
		st, err = store.OpenReadOnly(cfg.DBPath, 2)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer st.Close()
		fs := flag.NewFlagSet("token list", flag.ExitOnError)
		revoked := fs.Bool("revoked", false, "include revoked")
		_ = fs.Parse(rest)
		list, err := st.ListTokens(ctx, *revoked)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(list)
		return 0
	case "logs":
		_ = st.Close()
		st, err = store.OpenReadOnly(cfg.DBPath, 2)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer st.Close()
		fs := flag.NewFlagSet("token logs", flag.ExitOnError)
		tid := fs.String("token-id", "", "filter by token id")
		limit := fs.Int("limit", 50, "max rows")
		_ = fs.Parse(rest)
		logs, err := st.ListAccessLogs(ctx, *tid, *limit)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(logs)
		return 0
	default:
		printHelp()
		return 2
	}
}

func openStore(cfg config.Config) (*store.Store, error) {
	_ = os.MkdirAll(cfg.DataDir, 0o755)
	_ = os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755)
	return store.Open(cfg.DBPath, cfg.Workers.FetchWorkers)
}

func stringsHasPrefix(s, p string) bool {
	return len(s) >= len(p) && s[:len(p)] == p
}

func stringsTrimPrefix(s, p string) string {
	if stringsHasPrefix(s, p) {
		return s[len(p):]
	}
	return s
}

// silence unused import if duration used
var _ = time.Second
