package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/Quad4-Software/rss-discovery/internal/auth"
)

const authSchemaSQL = `
CREATE TABLE IF NOT EXISTS api_tokens (
	id VARCHAR PRIMARY KEY,
	name VARCHAR NOT NULL,
	level VARCHAR NOT NULL,
	prefix VARCHAR NOT NULL,
	hash VARCHAR NOT NULL UNIQUE,
	created_at TIMESTAMP DEFAULT now(),
	expires_at TIMESTAMP,
	revoked_at TIMESTAMP,
	last_used_at TIMESTAMP,
	use_count BIGINT DEFAULT 0
);

CREATE INDEX IF NOT EXISTS api_tokens_hash_idx ON api_tokens(hash);
CREATE INDEX IF NOT EXISTS api_tokens_level_idx ON api_tokens(level);

CREATE TABLE IF NOT EXISTS api_access_logs (
	id VARCHAR PRIMARY KEY,
	token_id VARCHAR,
	level VARCHAR,
	method VARCHAR,
	path VARCHAR,
	status INTEGER,
	ip VARCHAR,
	ua VARCHAR,
	latency_ms BIGINT,
	created_at TIMESTAMP DEFAULT now()
);

CREATE INDEX IF NOT EXISTS api_access_logs_token_idx ON api_access_logs(token_id);
CREATE INDEX IF NOT EXISTS api_access_logs_created_idx ON api_access_logs(created_at);
`

func (s *Store) migrateAuth(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, authSchemaSQL)
	return err
}

// CreateToken persists a new token record.
func (s *Store) CreateToken(ctx context.Context, rec auth.TokenRecord) error {
	return s.WithWrite(func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO api_tokens (id, name, level, prefix, hash, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, rec.ID, rec.Name, string(rec.Level), rec.Prefix, rec.Hash, rec.CreatedAt, rec.ExpiresAt)
		return err
	})
}

// LookupTokenByHash returns an active token for the given secret hash.
func (s *Store) LookupTokenByHash(ctx context.Context, hash string) (*auth.TokenRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, level, prefix, hash, created_at, expires_at, revoked_at, last_used_at, use_count
		FROM api_tokens WHERE hash=?
	`, hash)
	return scanToken(row)
}

func scanToken(row *sql.Row) (*auth.TokenRecord, error) {
	var rec auth.TokenRecord
	var level string
	var exp, rev, used sql.NullTime
	err := row.Scan(&rec.ID, &rec.Name, &level, &rec.Prefix, &rec.Hash, &rec.CreatedAt, &exp, &rev, &used, &rec.UseCount)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rec.Level = auth.Level(level)
	if exp.Valid {
		t := exp.Time.UTC()
		rec.ExpiresAt = &t
	}
	if rev.Valid {
		t := rev.Time.UTC()
		rec.RevokedAt = &t
	}
	if used.Valid {
		t := used.Time.UTC()
		rec.LastUsedAt = &t
	}
	rec.CreatedAt = rec.CreatedAt.UTC()
	return &rec, nil
}

// TouchToken updates last-used stats asynchronously-safe under write lock.
func (s *Store) TouchToken(ctx context.Context, id string) error {
	return s.WithWrite(func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE api_tokens SET last_used_at=now(), use_count=use_count+1 WHERE id=?
		`, id)
		return err
	})
}

// RevokeToken marks a token revoked.
func (s *Store) RevokeToken(ctx context.Context, id string) (bool, error) {
	var n int64
	err := s.WithWrite(func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `
			UPDATE api_tokens SET revoked_at=now() WHERE id=? AND revoked_at IS NULL
		`, id)
		if err != nil {
			return err
		}
		n, _ = r.RowsAffected()
		return nil
	})
	return n > 0, err
}

// ListTokens returns token metadata (no hashes).
func (s *Store) ListTokens(ctx context.Context, includeRevoked bool) ([]auth.TokenRecord, error) {
	q := `
		SELECT id, name, level, prefix, hash, created_at, expires_at, revoked_at, last_used_at, use_count
		FROM api_tokens`
	if !includeRevoked {
		q += ` WHERE revoked_at IS NULL`
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []auth.TokenRecord
	for rows.Next() {
		var rec auth.TokenRecord
		var level string
		var exp, rev, used sql.NullTime
		if err := rows.Scan(&rec.ID, &rec.Name, &level, &rec.Prefix, &rec.Hash, &rec.CreatedAt, &exp, &rev, &used, &rec.UseCount); err != nil {
			return nil, err
		}
		rec.Level = auth.Level(level)
		rec.Hash = ""
		if exp.Valid {
			t := exp.Time.UTC()
			rec.ExpiresAt = &t
		}
		if rev.Valid {
			t := rev.Time.UTC()
			rec.RevokedAt = &t
		}
		if used.Valid {
			t := used.Time.UTC()
			rec.LastUsedAt = &t
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// InsertAccessLog writes an API audit row. UA is truncated for privacy.
func (s *Store) InsertAccessLog(ctx context.Context, log auth.AccessLog) error {
	ua := log.UA
	if len(ua) > 120 {
		ua = ua[:120]
	}
	id := logID()
	return s.WithWrite(func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO api_access_logs (id, token_id, level, method, path, status, ip, ua, latency_ms, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, coalesce(?, now()))
		`, id, nullStr(log.TokenID), nullStr(log.Level), log.Method, log.Path, log.Status, log.IP, ua, log.LatencyMs, nullableTime(log.CreatedAt))
		return err
	})
}

func logID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// ListAccessLogs returns recent access logs.
func (s *Store) ListAccessLogs(ctx context.Context, tokenID string, limit int) ([]auth.AccessLog, error) {
	if limit < 1 {
		limit = 50
	}
	var rows *sql.Rows
	var err error
	if tokenID != "" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, COALESCE(token_id,''), COALESCE(level,''), method, path, status, COALESCE(ip,''), COALESCE(ua,''), latency_ms, created_at
			FROM api_access_logs WHERE token_id=? ORDER BY created_at DESC LIMIT ?
		`, tokenID, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, COALESCE(token_id,''), COALESCE(level,''), method, path, status, COALESCE(ip,''), COALESCE(ua,''), latency_ms, created_at
			FROM api_access_logs ORDER BY created_at DESC LIMIT ?
		`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []auth.AccessLog
	for rows.Next() {
		var l auth.AccessLog
		if err := rows.Scan(&l.ID, &l.TokenID, &l.Level, &l.Method, &l.Path, &l.Status, &l.IP, &l.UA, &l.LatencyMs, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ActiveToken validates expiry/revocation.
func ActiveToken(rec *auth.TokenRecord) error {
	if rec == nil {
		return fmt.Errorf("invalid token")
	}
	if rec.RevokedAt != nil {
		return fmt.Errorf("token revoked")
	}
	if rec.ExpiresAt != nil && time.Now().UTC().After(*rec.ExpiresAt) {
		return fmt.Errorf("token expired")
	}
	return nil
}
