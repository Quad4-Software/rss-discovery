package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is the process configuration loaded from TOML, flags, and env.
type Config struct {
	DataDir  string `toml:"data_dir"`
	DBPath   string `toml:"db_path"`
	SeedDir  string `toml:"seed_dir"`
	Landlock bool   `toml:"landlock"`

	Server   ServerConfig   `toml:"server"`
	Workers  WorkersConfig  `toml:"workers"`
	Limits   LimitsConfig   `toml:"limits"`
	S3       S3Config       `toml:"s3"`
	Search   SearchConfig   `toml:"search"`
	Auth     AuthConfig     `toml:"auth"`
	Observ   ObservConfig   `toml:"observability"`
	Security SecurityConfig `toml:"security"`
	Metrics  MetricsConfig  `toml:"metrics"`
	CORS     CORSConfig     `toml:"cors"`
	OAuth    OAuthConfig    `toml:"oauth"`
	WebSub   WebSubConfig   `toml:"websub"`
	Cache    CacheConfig    `toml:"cache"`
	Freshness FreshnessConfig `toml:"freshness"`
}

type ServerConfig struct {
	Addr                 string `toml:"addr"`
	ReadTimeoutSec       int    `toml:"read_timeout_sec"`
	ReadHeaderTimeoutSec int    `toml:"read_header_timeout_sec"`
	WriteTimeoutSec      int    `toml:"write_timeout_sec"`
	IdleTimeoutSec       int    `toml:"idle_timeout_sec"`
	BodyLimit            string `toml:"body_limit"`
	MaxHeaderBytes       int    `toml:"max_header_bytes"`
	MaxConcurrent        int64  `toml:"max_concurrent"`
	MaxConnPerIP         int    `toml:"max_conn_per_ip"`
}

type WorkersConfig struct {
	FetchWorkers          int    `toml:"fetch_workers"`
	ExtractWorkers        int    `toml:"extract_workers"`
	QueueSize             int    `toml:"queue_size"`
	HTTPTimeoutSec        int    `toml:"http_timeout_sec"`
	HTTPMaxIdle           int    `toml:"http_max_idle"`
	UserAgent             string `toml:"user_agent"`
	MaxFeedBytes          int64  `toml:"max_feed_bytes"`
	MaxArticleBytes       int64  `toml:"max_article_bytes"`
	ConcurrencyPerHost    int    `toml:"concurrency_per_host"`
	RequestsPerHostPerMin int    `toml:"requests_per_host_per_min"`
	MinHostGapMs          int    `toml:"min_host_gap_ms"`
}

type LimitsConfig struct {
	MaxTotalBytes     int64 `toml:"max_total_bytes"`
	MaxEntriesPerFeed int   `toml:"max_entries_per_feed"`
	MaxFulltextBytes  int64 `toml:"max_fulltext_bytes"`
	EntryTTLHours     int   `toml:"entry_ttl_hours"`
	FulltextTTLHours  int   `toml:"fulltext_ttl_hours"`
	FaviconTTLHours   int   `toml:"favicon_ttl_hours"`
	PurgeIntervalMin  int   `toml:"purge_interval_min"`
	PurgeBatch        int   `toml:"purge_batch"`
}

type S3Config struct {
	Enabled        bool   `toml:"enabled"`
	Bucket         string `toml:"bucket"`
	Prefix         string `toml:"prefix"`
	Region         string `toml:"region"`
	Endpoint       string `toml:"endpoint"`
	ForcePathStyle bool   `toml:"force_path_style"`
	AccessKey      string `toml:"access_key"`
	SecretKey      string `toml:"secret_key"`
}

type SearchConfig struct {
	FTSEnabled    bool    `toml:"fts_enabled"`
	SimilarityMin float64 `toml:"similarity_min"`
	DefaultLimit  int     `toml:"default_limit"`
	MaxLimit      int     `toml:"max_limit"`
}

type AuthConfig struct {
	Required                 bool   `toml:"required"`
	Header                   string `toml:"header"`
	QueryParam               string `toml:"query_param"`
	AllowAnonymous           bool   `toml:"allow_anonymous_health"`
	PublicRatePerMin         int    `toml:"public_rate_per_min"`
	PublicBurst              int    `toml:"public_burst"`
	AuthedReservedConcurrent int64  `toml:"authed_reserved_concurrent"`
	PublicMaxConnPerIP       int    `toml:"public_max_conn_per_ip"`
}

type ObservConfig struct {
	LogFile      string `toml:"log_file"`
	LogLevel     string `toml:"log_level"`
	SentryDSN    string `toml:"sentry_dsn"`
	SentryEnv    string `toml:"sentry_env"`
	PrivacyMode  bool   `toml:"privacy_mode"`
	OTLPEndpoint string `toml:"otlp_endpoint"`
	AccessLog    bool   `toml:"access_log"`
}

type SecurityConfig struct {
	Maintenance         bool   `toml:"maintenance"`
	BlockProbes         bool   `toml:"block_probes"`
	BlockScanners       bool   `toml:"block_scanners"`
	BlockScrapers       bool   `toml:"block_scrapers"`
	TrustProxy          bool   `toml:"trust_proxy"`
	RequireUA           bool   `toml:"require_user_agent"`
	BlocklistFile       string `toml:"blocklist_file"`
	BlocklistURL        string `toml:"blocklist_url"`
	BlocklistReloadMin  int    `toml:"blocklist_reload_min"`
	ValidateUpstreamURL bool   `toml:"validate_upstream_url"`
}

type MetricsConfig struct {
	Addr        string `toml:"addr"`
	Path        string `toml:"path"`
	AuthToken   string `toml:"auth_token"`
	RequireAuth bool   `toml:"require_auth"`
}

type CORSConfig struct {
	Enabled        bool     `toml:"enabled"`
	AllowOrigins   []string `toml:"allow_origins"`
	AllowHeaders   []string `toml:"allow_headers"`
	AllowMethods   []string `toml:"allow_methods"`
	AllowCredentials bool   `toml:"allow_credentials"`
	MaxAgeSec      int      `toml:"max_age_sec"`
}

type OAuthConfig struct {
	Enabled       bool   `toml:"enabled"`
	TokenTTLSec   int    `toml:"token_ttl_sec"`
	DefaultLevel  string `toml:"default_level"`
}

type WebSubConfig struct {
	Enabled      bool   `toml:"enabled"`
	CallbackURL  string `toml:"callback_url"`
	LeaseSeconds int    `toml:"lease_seconds"`
	RenewAheadSec int   `toml:"renew_ahead_sec"`
}

type CacheConfig struct {
	CDNEnabled            bool `toml:"cdn_enabled"`
	MaxAgeSec             int  `toml:"max_age_sec"`
	StaleWhileRevalidate  int  `toml:"stale_while_revalidate_sec"`
}

type FreshnessConfig struct {
	SLOSeconds int `toml:"slo_seconds"`
}

// Default returns sane defaults with a 1 GiB storage budget.
func Default() Config {
	return Config{
		DataDir:  "./data",
		DBPath:   "./data/rss.duckdb",
		SeedDir:  "./data/seeds",
		Landlock: true,
		Server: ServerConfig{
			Addr:                 "127.0.0.1:8787",
			ReadTimeoutSec:       15,
			ReadHeaderTimeoutSec: 5,
			WriteTimeoutSec:      30,
			IdleTimeoutSec:       60,
			BodyLimit:            "4M",
			MaxHeaderBytes:       64 << 10,
			MaxConcurrent:        4096,
			MaxConnPerIP:         64,
		},
		Workers: WorkersConfig{
			FetchWorkers:          8,
			ExtractWorkers:        4,
			QueueSize:             2048,
			HTTPTimeoutSec:        20,
			HTTPMaxIdle:           64,
			UserAgent:             "Mozilla/5.0 (compatible; rss-discovery/1.0; +https://github.com/Quad4-Software/rss-discovery)",
			MaxFeedBytes:          5 << 20,
			MaxArticleBytes:       2 << 20,
			ConcurrencyPerHost:    2,
			RequestsPerHostPerMin: 30,
			MinHostGapMs:          200,
		},
		Limits: LimitsConfig{
			MaxTotalBytes:     1 << 30,
			MaxEntriesPerFeed: 200,
			MaxFulltextBytes:  500 << 20,
			EntryTTLHours:     720,
			FulltextTTLHours:  336,
			FaviconTTLHours:   2160,
			PurgeIntervalMin:  15,
			PurgeBatch:        5000,
		},
		S3: S3Config{
			Region: "us-east-1",
			Prefix: "rss-discovery/",
		},
		Search: SearchConfig{
			FTSEnabled:    true,
			SimilarityMin: 0.25,
			DefaultLimit:  25,
			MaxLimit:      100,
		},
		Auth: AuthConfig{
			Required:                 true,
			Header:                   "Authorization",
			QueryParam:               "api_key",
			AllowAnonymous:           true,
			PublicRatePerMin:         60,
			PublicBurst:              10,
			AuthedReservedConcurrent: 1024,
			PublicMaxConnPerIP:       8,
		},
		Observ: ObservConfig{
			LogLevel:    "info",
			PrivacyMode: true,
			AccessLog:   true,
			SentryEnv:   "production",
		},
		Security: SecurityConfig{
			BlockProbes:         true,
			BlockScanners:       true,
			BlockScrapers:       true,
			RequireUA:           true,
			BlocklistFile:       "./data/blocklist.txt",
			BlocklistReloadMin:  15,
			ValidateUpstreamURL: true,
		},
		Metrics: MetricsConfig{
			Addr:        "127.0.0.1:8788",
			Path:        "/metrics",
			RequireAuth: true,
		},
		CORS: CORSConfig{
			Enabled:          true,
			AllowOrigins:     []string{"*"},
			AllowHeaders:     []string{"Authorization", "Content-Type", "X-API-Key", "If-None-Match", "If-Modified-Since"},
			AllowMethods:     []string{"GET", "POST", "DELETE", "OPTIONS", "HEAD"},
			AllowCredentials: false,
			MaxAgeSec:        600,
		},
		OAuth: OAuthConfig{
			Enabled:      true,
			TokenTTLSec:  3600,
			DefaultLevel: "standard",
		},
		WebSub: WebSubConfig{
			Enabled:       true,
			CallbackURL:   "",
			LeaseSeconds:  86400,
			RenewAheadSec: 3600,
		},
		Cache: CacheConfig{
			CDNEnabled:           true,
			MaxAgeSec:            30,
			StaleWhileRevalidate: 60,
		},
		Freshness: FreshnessConfig{
			SLOSeconds: 3600,
		},
	}
}

func (c Config) FreshnessSLO() time.Duration {
	sec := c.Freshness.SLOSeconds
	if sec < 60 {
		sec = 3600
	}
	return time.Duration(sec) * time.Second
}

func (c Config) OAuthTTL() time.Duration {
	sec := c.OAuth.TokenTTLSec
	if sec < 60 {
		sec = 3600
	}
	return time.Duration(sec) * time.Second
}

func (c Config) HTTPTimeout() time.Duration {
	return time.Duration(c.Workers.HTTPTimeoutSec) * time.Second
}

func (c Config) PurgeInterval() time.Duration {
	return time.Duration(c.Limits.PurgeIntervalMin) * time.Minute
}

func (c Config) EntryTTL() time.Duration {
	return time.Duration(c.Limits.EntryTTLHours) * time.Hour
}

func (c Config) FulltextTTL() time.Duration {
	return time.Duration(c.Limits.FulltextTTLHours) * time.Hour
}

// Load reads TOML from path if non-empty, then applies flags and RSS_DISCOVERY_* env.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		if _, err := toml.DecodeFile(path, &cfg); err != nil {
			return cfg, fmt.Errorf("toml: %w", err)
		}
	}
	applyEnv(&cfg)
	return cfg, cfg.Validate()
}

// BindFlags registers CLI overrides.
func BindFlags(fs *flag.FlagSet, cfg *Config, configPath *string) {
	fs.StringVar(configPath, "config", "config.toml", "path to TOML config")
	fs.StringVar(&cfg.Server.Addr, "addr", cfg.Server.Addr, "listen address")
	fs.StringVar(&cfg.DBPath, "db", cfg.DBPath, "DuckDB database path")
	fs.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "local data directory")
	fs.StringVar(&cfg.SeedDir, "seed-dir", cfg.SeedDir, "seed files directory")
	fs.BoolVar(&cfg.Landlock, "landlock", cfg.Landlock, "enable Landlock sandbox")
	fs.IntVar(&cfg.Workers.FetchWorkers, "workers", cfg.Workers.FetchWorkers, "feed fetch workers")
	fs.Int64Var(&cfg.Limits.MaxTotalBytes, "max-bytes", cfg.Limits.MaxTotalBytes, "max total storage bytes")
	fs.BoolVar(&cfg.S3.Enabled, "s3", cfg.S3.Enabled, "enable S3 blob storage")
	fs.BoolVar(&cfg.Auth.Required, "auth-required", cfg.Auth.Required, "require API tokens (false enables public read mode)")
	fs.BoolVar(&cfg.Security.Maintenance, "maintenance", cfg.Security.Maintenance, "enable maintenance mode")
	fs.StringVar(&cfg.Observ.LogFile, "log-file", cfg.Observ.LogFile, "log file path")
	fs.StringVar(&cfg.Observ.LogLevel, "log-level", cfg.Observ.LogLevel, "log level")
	fs.StringVar(&cfg.Observ.SentryDSN, "sentry-dsn", cfg.Observ.SentryDSN, "Sentry/GlitchTip/BugSinks DSN")
	fs.BoolVar(&cfg.Observ.PrivacyMode, "privacy", cfg.Observ.PrivacyMode, "privacy-friendly logging")
	fs.StringVar(&cfg.Observ.OTLPEndpoint, "otlp", cfg.Observ.OTLPEndpoint, "OTLP HTTP endpoint")
	fs.StringVar(&cfg.Metrics.Addr, "metrics-addr", cfg.Metrics.Addr, "metrics listen address")
	fs.StringVar(&cfg.Metrics.AuthToken, "metrics-token", cfg.Metrics.AuthToken, "metrics bearer token")
}

func (c *Config) Validate() error {
	if c.Server.Addr == "" {
		return fmt.Errorf("server.addr required")
	}
	if c.DBPath == "" {
		return fmt.Errorf("db_path required")
	}
	if c.Limits.MaxTotalBytes <= 0 {
		return fmt.Errorf("limits.max_total_bytes must be > 0")
	}
	if c.Workers.FetchWorkers < 1 {
		return fmt.Errorf("workers.fetch_workers must be >= 1")
	}
	if c.Search.DefaultLimit < 1 {
		c.Search.DefaultLimit = 25
	}
	if c.CORS.AllowCredentials {
		for _, o := range c.CORS.AllowOrigins {
			if o == "*" {
				return fmt.Errorf("cors: allow_credentials cannot be used with allow_origins=*")
			}
		}
	}
	if c.Auth.PublicRatePerMin < 1 {
		c.Auth.PublicRatePerMin = 60
	}
	if c.Auth.PublicBurst < 1 {
		c.Auth.PublicBurst = 10
	}
	if c.Auth.AuthedReservedConcurrent < 0 {
		c.Auth.AuthedReservedConcurrent = 0
	}
	if c.Auth.PublicMaxConnPerIP < 1 {
		c.Auth.PublicMaxConnPerIP = 8
	}
	return nil
}

func applyEnv(cfg *Config) {
	envStr("RSS_DISCOVERY_DATA_DIR", &cfg.DataDir)
	envStr("RSS_DISCOVERY_DB_PATH", &cfg.DBPath)
	envStr("RSS_DISCOVERY_SEED_DIR", &cfg.SeedDir)
	envBool("RSS_DISCOVERY_LANDLOCK", &cfg.Landlock)
	envStr("RSS_DISCOVERY_SERVER_ADDR", &cfg.Server.Addr)
	envInt("RSS_DISCOVERY_WORKERS_FETCH_WORKERS", &cfg.Workers.FetchWorkers)
	envInt("RSS_DISCOVERY_WORKERS_EXTRACT_WORKERS", &cfg.Workers.ExtractWorkers)
	envInt64("RSS_DISCOVERY_LIMITS_MAX_TOTAL_BYTES", &cfg.Limits.MaxTotalBytes)
	envInt64("RSS_DISCOVERY_LIMITS_MAX_FULLTEXT_BYTES", &cfg.Limits.MaxFulltextBytes)
	envInt("RSS_DISCOVERY_LIMITS_PURGE_INTERVAL_MIN", &cfg.Limits.PurgeIntervalMin)
	envBool("RSS_DISCOVERY_S3_ENABLED", &cfg.S3.Enabled)
	envStr("RSS_DISCOVERY_S3_BUCKET", &cfg.S3.Bucket)
	envStr("RSS_DISCOVERY_S3_PREFIX", &cfg.S3.Prefix)
	envStr("RSS_DISCOVERY_S3_REGION", &cfg.S3.Region)
	envStr("RSS_DISCOVERY_S3_ENDPOINT", &cfg.S3.Endpoint)
	envStr("RSS_DISCOVERY_S3_ACCESS_KEY", &cfg.S3.AccessKey)
	envStr("RSS_DISCOVERY_S3_SECRET_KEY", &cfg.S3.SecretKey)
	envStr("AWS_ACCESS_KEY_ID", &cfg.S3.AccessKey)
	envStr("AWS_SECRET_ACCESS_KEY", &cfg.S3.SecretKey)
	envBool("RSS_DISCOVERY_AUTH_REQUIRED", &cfg.Auth.Required)
	envBool("RSS_DISCOVERY_MAINTENANCE", &cfg.Security.Maintenance)
	envStr("RSS_DISCOVERY_LOG_FILE", &cfg.Observ.LogFile)
	envStr("RSS_DISCOVERY_LOG_LEVEL", &cfg.Observ.LogLevel)
	envStr("RSS_DISCOVERY_SENTRY_DSN", &cfg.Observ.SentryDSN)
	envStr("SENTRY_DSN", &cfg.Observ.SentryDSN)
	envStr("GLITCHTIP_DSN", &cfg.Observ.SentryDSN)
	envStr("BUGSINKS_DSN", &cfg.Observ.SentryDSN)
	envBool("RSS_DISCOVERY_PRIVACY", &cfg.Observ.PrivacyMode)
	envStr("RSS_DISCOVERY_OTLP_ENDPOINT", &cfg.Observ.OTLPEndpoint)
	envStr("OTEL_EXPORTER_OTLP_ENDPOINT", &cfg.Observ.OTLPEndpoint)
	envStr("RSS_DISCOVERY_METRICS_ADDR", &cfg.Metrics.Addr)
	envStr("RSS_DISCOVERY_METRICS_TOKEN", &cfg.Metrics.AuthToken)
}

func envStr(key string, dst *string) {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		*dst = v
	}
}

func envBool(key string, dst *bool) {
	if v, ok := os.LookupEnv(key); ok {
		switch strings.ToLower(v) {
		case "1", "true", "yes", "on":
			*dst = true
		case "0", "false", "no", "off":
			*dst = false
		}
	}
}

func envInt(key string, dst *int) {
	if v, ok := os.LookupEnv(key); ok {
		n, err := strconv.Atoi(v)
		if err == nil {
			*dst = n
		}
	}
}

func envInt64(key string, dst *int64) {
	if v, ok := os.LookupEnv(key); ok {
		n, err := strconv.ParseInt(v, 10, 64)
		if err == nil {
			*dst = n
		}
	}
}
