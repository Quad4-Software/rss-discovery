package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	duckdb "github.com/duckdb/duckdb-go/v2"
)

// Store wraps DuckDB with a single-writer mutex for crash-safe upserts.
type Store struct {
	db       *sql.DB
	connStr  string
	writeMu  sync.Mutex
	ftsReady bool
}

// Open creates or opens a persistent DuckDB database.
func Open(path string, threads int) (*Store, error) {
	return open(path, threads, false)
}

// OpenReadOnly opens DuckDB for concurrent read access alongside a writer.
func OpenReadOnly(path string, threads int) (*Store, error) {
	return open(path, threads, true)
}

func open(path string, threads int, readOnly bool) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if threads < 1 {
		threads = 4
	}
	mode := ""
	if readOnly {
		mode = "&access_mode=READ_ONLY"
	}
	connStr := fmt.Sprintf("%s?threads=%d%s", path, threads, mode)
	connector, err := duckdb.NewConnector(connStr, func(execer driver.ExecerContext) error {
		boot := []string{
			`SET enable_progress_bar=false`,
			`SET preserve_insertion_order=false`,
		}
		for _, q := range boot {
			if _, err := execer.ExecContext(context.Background(), q, nil); err != nil {
				slog.Debug("duckdb boot", "q", q, "err", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("duckdb connector: %w", err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(threads + 2)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(0)

	s := &Store{db: db, connStr: connStr}
	if !readOnly {
		if err := s.migrate(context.Background()); err != nil {
			_ = db.Close()
			return nil, err
		}
	} else {
		// FTS may still be usable if previously installed.
		_, _ = s.db.ExecContext(context.Background(), `LOAD fts`)
		s.ftsReady = true
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) WithWrite(fn func(tx *sql.Tx) error) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, schemaSQL)
	if err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	if err := s.initFTS(ctx); err != nil {
		slog.Warn("fts unavailable", "err", err)
	}
	if err := s.migrateAuth(ctx); err != nil {
		return fmt.Errorf("auth schema: %w", err)
	}
	if err := s.migrateFeatures(ctx); err != nil {
		return fmt.Errorf("features schema: %w", err)
	}
	return nil
}

func (s *Store) initFTS(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `INSTALL fts`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `LOAD fts`); err != nil {
		return err
	}
	// Recreate indexes if missing. Ignore errors when already present.
	_, _ = s.db.ExecContext(ctx, `
		PRAGMA create_fts_index('feeds', 'id', 'title', 'description', 'site_url', 'category', 'blurb', stemmer='porter', stopwords='english', ignore_empty=1)
	`)
	_, _ = s.db.ExecContext(ctx, `
		PRAGMA create_fts_index('entries', 'id', 'title', 'summary', 'author', 'url', stemmer='porter', stopwords='english', ignore_empty=1)
	`)
	_, _ = s.db.ExecContext(ctx, `
		PRAGMA create_fts_index('fulltext', 'entry_id', 'title', 'text', 'excerpt', stemmer='porter', stopwords='english', ignore_empty=1)
	`)
	s.ftsReady = true
	return nil
}

func (s *Store) FTSReady() bool { return s.ftsReady }

// FileSize returns on-disk DuckDB size in bytes.
func (s *Store) FileSize() (int64, error) {
	path := stringsTrimQuery(s.connStr)
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	var wal int64
	if w, err := os.Stat(path + ".wal"); err == nil {
		wal = w.Size()
	}
	return fi.Size() + wal, nil
}

func stringsTrimQuery(s string) string {
	if i := indexByte(s, '?'); i >= 0 {
		return s[:i]
	}
	return s
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// Checkpoint forces a WAL checkpoint for crash recovery durability.
func (s *Store) Checkpoint(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `CHECKPOINT`)
	return err
}

// Stats returns aggregate catalog metrics.
func (s *Store) Stats(ctx context.Context) (map[string]any, error) {
	out := map[string]any{}
	row := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM feeds),
			(SELECT COUNT(*) FROM feeds WHERE last_status >= 200 AND last_status < 400),
			(SELECT COUNT(*) FROM entries),
			(SELECT COUNT(*) FROM fulltext),
			(SELECT COUNT(*) FROM favicons),
			(SELECT COALESCE(SUM(byte_size),0) FROM fulltext),
			(SELECT COALESCE(SUM(byte_size),0) FROM favicons)
	`)
	var feeds, okFeeds, entries, fulltext, favicons, ftBytes, favBytes int64
	if err := row.Scan(&feeds, &okFeeds, &entries, &fulltext, &favicons, &ftBytes, &favBytes); err != nil {
		return nil, err
	}
	dbBytes, _ := s.FileSize()
	out["feeds"] = feeds
	out["feeds_ok"] = okFeeds
	out["entries"] = entries
	out["fulltext"] = fulltext
	out["favicons"] = favicons
	out["fulltext_bytes"] = ftBytes
	out["favicon_bytes"] = favBytes
	out["db_bytes"] = dbBytes
	out["fts"] = s.ftsReady
	out["checked_at"] = time.Now().UTC().Format(time.RFC3339)
	return out, nil
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS feeds (
	id VARCHAR PRIMARY KEY,
	url VARCHAR NOT NULL UNIQUE,
	site_url VARCHAR,
	title VARCHAR,
	description VARCHAR,
	category VARCHAR,
	blurb VARCHAR,
	format VARCHAR,
	language VARCHAR,
	favicon_url VARCHAR,
	etag VARCHAR,
	last_modified VARCHAR,
	last_status INTEGER DEFAULT 0,
	last_error VARCHAR,
	last_fetched_at TIMESTAMP,
	last_ok_at TIMESTAMP,
	entry_count INTEGER DEFAULT 0,
	fetch_count INTEGER DEFAULT 0,
	fail_count INTEGER DEFAULT 0,
	avg_latency_ms DOUBLE DEFAULT 0,
	priority DOUBLE DEFAULT 0,
	source VARCHAR,
	created_at TIMESTAMP DEFAULT now(),
	updated_at TIMESTAMP DEFAULT now()
);

CREATE TABLE IF NOT EXISTS entries (
	id VARCHAR PRIMARY KEY,
	feed_id VARCHAR NOT NULL,
	guid VARCHAR,
	url VARCHAR,
	title VARCHAR,
	summary VARCHAR,
	author VARCHAR,
	published_at TIMESTAMP,
	updated_at TIMESTAMP,
	image_url VARCHAR,
	content_hash VARCHAR,
	fetched_at TIMESTAMP DEFAULT now(),
	UNIQUE(feed_id, guid)
);

CREATE INDEX IF NOT EXISTS entries_feed_idx ON entries(feed_id);
CREATE INDEX IF NOT EXISTS entries_published_idx ON entries(published_at);
CREATE INDEX IF NOT EXISTS entries_url_idx ON entries(url);

CREATE TABLE IF NOT EXISTS fulltext (
	entry_id VARCHAR PRIMARY KEY,
	feed_id VARCHAR,
	url VARCHAR,
	title VARCHAR,
	text VARCHAR,
	excerpt VARCHAR,
	byline VARCHAR,
	site_name VARCHAR,
	image_url VARCHAR,
	storage VARCHAR DEFAULT 'local',
	blob_key VARCHAR,
	byte_size INTEGER DEFAULT 0,
	fetched_at TIMESTAMP DEFAULT now(),
	accessed_at TIMESTAMP DEFAULT now()
);

CREATE INDEX IF NOT EXISTS fulltext_accessed_idx ON fulltext(accessed_at);
CREATE INDEX IF NOT EXISTS fulltext_feed_idx ON fulltext(feed_id);

CREATE TABLE IF NOT EXISTS favicons (
	feed_id VARCHAR PRIMARY KEY,
	url VARCHAR,
	content_type VARCHAR,
	data BLOB,
	byte_size INTEGER DEFAULT 0,
	fetched_at TIMESTAMP DEFAULT now()
);

CREATE TABLE IF NOT EXISTS feed_stats (
	feed_id VARCHAR PRIMARY KEY,
	items_last INTEGER DEFAULT 0,
	bytes_last INTEGER DEFAULT 0,
	latency_ms_last INTEGER DEFAULT 0,
	ok_streak INTEGER DEFAULT 0,
	fail_streak INTEGER DEFAULT 0,
	updated_at TIMESTAMP DEFAULT now()
);

CREATE TABLE IF NOT EXISTS meta (
	key VARCHAR PRIMARY KEY,
	value VARCHAR
);
`
