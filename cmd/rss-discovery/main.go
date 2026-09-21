package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Quad4-Software/rss-discovery/internal/api"
	"github.com/Quad4-Software/rss-discovery/internal/blob"
	"github.com/Quad4-Software/rss-discovery/internal/config"
	"github.com/Quad4-Software/rss-discovery/internal/fetch"
	"github.com/Quad4-Software/rss-discovery/internal/observ"
	"github.com/Quad4-Software/rss-discovery/internal/sandbox"
	"github.com/Quad4-Software/rss-discovery/internal/security"
	"github.com/Quad4-Software/rss-discovery/internal/seed"
	"github.com/Quad4-Software/rss-discovery/internal/store"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "token":
			os.Exit(runToken(os.Args[2:]))
		case "serve":
			os.Exit(runServe(os.Args[2:]))
		case "help", "-h", "--help":
			printHelp()
			return
		}
	}
	os.Exit(runServe(os.Args[1:]))
}

func printHelp() {
	fmt.Fprintf(os.Stderr, `rss-discovery

Usage:
  rss-discovery serve [flags]
  rss-discovery token generate --name NAME --level LEVEL [--ttl 720h]
  rss-discovery token revoke ID
  rss-discovery token list [--revoked]
  rss-discovery token logs [--token-id ID] [--limit 50]

Levels: readonly | standard | priority | admin
`)
}

func runServe(args []string) int {
	var (
		doSeed    bool
		doFetch   int
		purgeOnly bool
		seedExit  bool
	)
	cfg := config.Default()
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	var configPath string
	config.BindFlags(fs, &cfg, &configPath)
	fs.BoolVar(&doSeed, "seed", false, "import seed catalogs into DuckDB")
	fs.BoolVar(&seedExit, "seed-exit", false, "exit after seeding")
	fs.IntVar(&doFetch, "fetch", 0, "refresh N due feeds then exit")
	fs.BoolVar(&purgeOnly, "purge", false, "run purge once and exit")
	_ = fs.Parse(args)

	if _, err := os.Stat(configPath); err == nil {
		loaded, err := config.Load(configPath)
		if err != nil {
			slog.Error("config", "err", err)
			return 1
		}
		cfg = loaded
		fs2 := flag.NewFlagSet("serve", flag.ContinueOnError)
		fs2.SetOutput(os.Stderr)
		var ignored string
		config.BindFlags(fs2, &cfg, &ignored)
		fs2.BoolVar(&doSeed, "seed", doSeed, "")
		fs2.BoolVar(&seedExit, "seed-exit", seedExit, "")
		fs2.IntVar(&doFetch, "fetch", doFetch, "")
		fs2.BoolVar(&purgeOnly, "purge", purgeOnly, "")
		_ = fs2.Parse(args)
	}
	if err := cfg.Validate(); err != nil {
		slog.Error("config invalid", "err", err)
		return 1
	}

	obs, err := observ.Setup(observ.Options{
		ServiceName:  "rss-discovery",
		LogFile:      cfg.Observ.LogFile,
		LogLevel:     cfg.Observ.LogLevel,
		SentryDSN:    cfg.Observ.SentryDSN,
		SentryEnv:    cfg.Observ.SentryEnv,
		PrivacyMode:  cfg.Observ.PrivacyMode,
		OTLPEndpoint: cfg.Observ.OTLPEndpoint,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer obs.Close(context.Background())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := openStore(cfg)
	if err != nil {
		slog.Error("duckdb open", "err", err)
		return 1
	}
	defer func() {
		_ = st.Checkpoint(context.Background())
		_ = st.Close()
	}()

	blobs, err := blob.New(cfg)
	if err != nil {
		slog.Error("blob store", "err", err)
		return 1
	}

	if err := sandbox.Apply(cfg.DataDir, cfg.DBPath, cfg.SeedDir, cfg.Server.Addr, cfg.Landlock); err != nil {
		slog.Error("landlock", "err", err)
		return 1
	}

	if doSeed {
		n, err := seed.LoadDir(ctx, st, cfg.SeedDir)
		if err != nil {
			slog.Error("seed", "err", err)
			return 1
		}
		stats, _ := st.Stats(ctx)
		slog.Info("seed complete", "upserted_sources", n, "stats", stats)
		if seedExit {
			return 0
		}
	}

	pool := fetch.NewPool(cfg, st, blobs)
	pool.Start(ctx)
	defer pool.Stop()

	if purgeOnly {
		res, err := st.Purge(ctx, cfg.Limits.MaxTotalBytes, cfg.Limits.MaxFulltextBytes,
			cfg.EntryTTL(), cfg.FulltextTTL(), time.Duration(cfg.Limits.FaviconTTLHours)*time.Hour, cfg.Limits.PurgeBatch)
		if err != nil {
			slog.Error("purge", "err", err)
			return 1
		}
		fmt.Printf("%+v\n", res)
		return 0
	}

	if doFetch > 0 {
		feeds, err := st.ListFeedsDue(ctx, doFetch, time.Hour)
		if err != nil {
			slog.Error("list due", "err", err)
			return 1
		}
		for _, f := range feeds {
			if err := pool.RefreshFeed(ctx, f.ID); err != nil {
				slog.Warn("refresh", "url", f.URL, "err", err)
			}
		}
		return 0
	}

	go purgeLoop(ctx, st, cfg)

	if cfg.Security.BlocklistFile != "" || cfg.Security.BlocklistURL != "" {
		reloadEvery := time.Duration(cfg.Security.BlocklistReloadMin) * time.Minute
		security.StartReloader(ctx, cfg.Security.BlocklistFile, cfg.Security.BlocklistURL, reloadEvery)
	}

	srv := api.New(cfg, st, pool, obs)
	go func() {
		slog.Info("listening",
			"addr", cfg.Server.Addr,
			"metrics", cfg.Metrics.Addr,
			"auth_required", cfg.Auth.Required,
			"max_bytes", cfg.Limits.MaxTotalBytes,
		)
		if err := srv.Start(); err != nil && !strings.Contains(err.Error(), "Server closed") {
			slog.Error("server", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	_ = st.Checkpoint(shutdownCtx)
	slog.Info("shutdown complete")
	return 0
}

func purgeLoop(ctx context.Context, st *store.Store, cfg config.Config) {
	t := time.NewTicker(cfg.PurgeInterval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = st.Purge(ctx, cfg.Limits.MaxTotalBytes, cfg.Limits.MaxFulltextBytes,
				cfg.EntryTTL(), cfg.FulltextTTL(), time.Duration(cfg.Limits.FaviconTTLHours)*time.Hour, cfg.Limits.PurgeBatch)
			_ = st.Checkpoint(ctx)
		}
	}
}
