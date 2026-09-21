package store

import (
	"context"
	"database/sql"
	"fmt"
)

const featuresSchemaSQL = `
CREATE TABLE IF NOT EXISTS async_jobs (
	id VARCHAR PRIMARY KEY,
	type VARCHAR NOT NULL,
	status VARCHAR NOT NULL,
	total INTEGER DEFAULT 0,
	done_count INTEGER DEFAULT 0,
	failed_count INTEGER DEFAULT 0,
	error VARCHAR,
	result_json VARCHAR,
	created_by VARCHAR,
	created_at TIMESTAMP DEFAULT now(),
	updated_at TIMESTAMP DEFAULT now()
);

CREATE TABLE IF NOT EXISTS async_job_items (
	id VARCHAR PRIMARY KEY,
	job_id VARCHAR NOT NULL,
	url VARCHAR NOT NULL,
	status VARCHAR NOT NULL,
	feed_id VARCHAR,
	error VARCHAR,
	created_at TIMESTAMP DEFAULT now(),
	updated_at TIMESTAMP DEFAULT now()
);
CREATE INDEX IF NOT EXISTS async_job_items_job_idx ON async_job_items(job_id, status);

CREATE TABLE IF NOT EXISTS webhooks (
	id VARCHAR PRIMARY KEY,
	url VARCHAR NOT NULL,
	secret VARCHAR NOT NULL,
	events VARCHAR NOT NULL,
	feed_id VARCHAR,
	active BOOLEAN DEFAULT true,
	fail_count INTEGER DEFAULT 0,
	last_status INTEGER DEFAULT 0,
	last_error VARCHAR,
	last_sent_at TIMESTAMP,
	created_by VARCHAR,
	created_at TIMESTAMP DEFAULT now(),
	updated_at TIMESTAMP DEFAULT now()
);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
	id VARCHAR PRIMARY KEY,
	webhook_id VARCHAR NOT NULL,
	event VARCHAR NOT NULL,
	payload VARCHAR NOT NULL,
	status INTEGER DEFAULT 0,
	error VARCHAR,
	created_at TIMESTAMP DEFAULT now()
);

CREATE TABLE IF NOT EXISTS websub_subs (
	id VARCHAR PRIMARY KEY,
	feed_id VARCHAR NOT NULL UNIQUE,
	topic VARCHAR NOT NULL,
	hub VARCHAR NOT NULL,
	secret VARCHAR NOT NULL,
	lease_seconds INTEGER DEFAULT 86400,
	status VARCHAR NOT NULL,
	expires_at TIMESTAMP,
	last_error VARCHAR,
	created_at TIMESTAMP DEFAULT now(),
	updated_at TIMESTAMP DEFAULT now()
);

CREATE TABLE IF NOT EXISTS oauth_clients (
	id VARCHAR PRIMARY KEY,
	name VARCHAR NOT NULL,
	client_id VARCHAR NOT NULL UNIQUE,
	client_secret_hash VARCHAR NOT NULL,
	level VARCHAR NOT NULL,
	created_at TIMESTAMP DEFAULT now(),
	revoked_at TIMESTAMP
);

CREATE TABLE IF NOT EXISTS oauth_tokens (
	id VARCHAR PRIMARY KEY,
	client_id VARCHAR NOT NULL,
	token_hash VARCHAR NOT NULL UNIQUE,
	level VARCHAR NOT NULL,
	expires_at TIMESTAMP NOT NULL,
	created_at TIMESTAMP DEFAULT now(),
	revoked_at TIMESTAMP
);

CREATE TABLE IF NOT EXISTS feed_embeddings (
	feed_id VARCHAR PRIMARY KEY,
	dims INTEGER NOT NULL,
	vector_json VARCHAR NOT NULL,
	updated_at TIMESTAMP DEFAULT now()
);

CREATE TABLE IF NOT EXISTS feed_latency_samples (
	id VARCHAR PRIMARY KEY,
	feed_id VARCHAR NOT NULL,
	latency_ms INTEGER NOT NULL,
	ok BOOLEAN NOT NULL,
	created_at TIMESTAMP DEFAULT now()
);

CREATE TABLE IF NOT EXISTS feed_format_history (
	id VARCHAR PRIMARY KEY,
	feed_id VARCHAR NOT NULL,
	format VARCHAR NOT NULL,
	created_at TIMESTAMP DEFAULT now()
);

CREATE INDEX IF NOT EXISTS feed_latency_feed_idx ON feed_latency_samples(feed_id, created_at);
CREATE INDEX IF NOT EXISTS feed_format_feed_idx ON feed_format_history(feed_id, created_at);
`

func (s *Store) migrateFeatures(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, featuresSchemaSQL); err != nil {
		return fmt.Errorf("features schema: %w", err)
	}
	_, _ = s.db.ExecContext(ctx, `ALTER TABLE webhooks ADD COLUMN IF NOT EXISTS created_by VARCHAR`)
	_, _ = s.db.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES ('catalog_rev', '1') ON CONFLICT (key) DO NOTHING`)
	return nil
}

// BumpCatalogRev increments catalog revision used for API ETags.
func (s *Store) BumpCatalogRev(ctx context.Context) error {
	return s.WithWrite(func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO meta (key, value) VALUES ('catalog_rev', '1')
			ON CONFLICT (key) DO UPDATE SET value = CAST(CAST(meta.value AS BIGINT)+1 AS VARCHAR)
		`)
		return err
	})
}

// CatalogRev returns the current catalog revision string.
func (s *Store) CatalogRev(ctx context.Context) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='catalog_rev'`).Scan(&v)
	if err == sql.ErrNoRows {
		return "0", nil
	}
	return v, err
}

// CatalogUpdatedAt returns max feed updated_at for Last-Modified.
func (s *Store) CatalogUpdatedAt(ctx context.Context) (string, error) {
	var v sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT strftime(MAX(updated_at), '%Y-%m-%dT%H:%M:%SZ') FROM feeds`).Scan(&v)
	if err != nil {
		return "", err
	}
	if !v.Valid || v.String == "" {
		return "1970-01-01T00:00:00Z", nil
	}
	return v.String, nil
}
