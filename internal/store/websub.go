package store

import (
	"context"
	"database/sql"
	"time"
)

// WebSubSub is a hub subscription for a feed.
type WebSubSub struct {
	ID          string     `json:"id"`
	FeedID      string     `json:"feed_id"`
	Topic       string     `json:"topic"`
	Hub         string     `json:"hub"`
	Secret      string     `json:"-"`
	LeaseSecs   int        `json:"lease_seconds"`
	Status      string     `json:"status"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

const (
	WebSubPending   = "pending"
	WebSubActive    = "active"
	WebSubDenied    = "denied"
	WebSubExpired   = "expired"
)

// UpsertWebSub stores or updates a subscription row.
func (s *Store) UpsertWebSub(ctx context.Context, sub WebSubSub) error {
	if sub.ID == "" {
		sub.ID = newID()
	}
	now := time.Now().UTC()
	return s.WithWrite(func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO websub_subs (id, feed_id, topic, hub, secret, lease_seconds, status, expires_at, last_error, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (feed_id) DO UPDATE SET
				topic=excluded.topic,
				hub=excluded.hub,
				secret=excluded.secret,
				lease_seconds=excluded.lease_seconds,
				status=excluded.status,
				expires_at=excluded.expires_at,
				last_error=excluded.last_error,
				updated_at=excluded.updated_at
		`, sub.ID, sub.FeedID, sub.Topic, sub.Hub, sub.Secret, sub.LeaseSecs, sub.Status, sub.ExpiresAt,
			nullStr(sub.LastError), now, now)
		return err
	})
}

// GetWebSubByFeed returns subscription for feed.
func (s *Store) GetWebSubByFeed(ctx context.Context, feedID string) (*WebSubSub, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, feed_id, topic, hub, secret, lease_seconds, status, expires_at, COALESCE(last_error,''), created_at, updated_at
		FROM websub_subs WHERE feed_id=?
	`, feedID)
	return scanWebSub(row)
}

// GetWebSubByTopic finds by topic URL.
func (s *Store) GetWebSubByTopic(ctx context.Context, topic string) (*WebSubSub, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, feed_id, topic, hub, secret, lease_seconds, status, expires_at, COALESCE(last_error,''), created_at, updated_at
		FROM websub_subs WHERE topic=?
	`, topic)
	return scanWebSub(row)
}

// ListWebSubs needing renew (expires soon or pending).
func (s *Store) ListWebSubsDue(ctx context.Context, within time.Duration, limit int) ([]WebSubSub, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, feed_id, topic, hub, secret, lease_seconds, status, expires_at, COALESCE(last_error,''), created_at, updated_at
		FROM websub_subs
		WHERE status IN (?, ?) OR expires_at IS NULL OR expires_at < (now() + (? * INTERVAL '1 second'))
		ORDER BY COALESCE(expires_at, TIMESTAMP '1970-01-01') ASC
		LIMIT ?
	`, WebSubPending, WebSubExpired, int(within.Seconds()), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WebSubSub
	for rows.Next() {
		sub, err := scanWebSubRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sub)
	}
	return out, rows.Err()
}

// ListWebSubs all.
func (s *Store) ListWebSubs(ctx context.Context, limit int) ([]WebSubSub, error) {
	if limit < 1 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, feed_id, topic, hub, secret, lease_seconds, status, expires_at, COALESCE(last_error,''), created_at, updated_at
		FROM websub_subs ORDER BY updated_at DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WebSubSub
	for rows.Next() {
		sub, err := scanWebSubRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sub)
	}
	return out, rows.Err()
}

// DeleteWebSubByFeed removes subscription.
func (s *Store) DeleteWebSubByFeed(ctx context.Context, feedID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM websub_subs WHERE feed_id=?`, feedID)
	return err
}

func scanWebSub(row *sql.Row) (*WebSubSub, error) {
	var sub WebSubSub
	var exp sql.NullTime
	err := row.Scan(&sub.ID, &sub.FeedID, &sub.Topic, &sub.Hub, &sub.Secret, &sub.LeaseSecs, &sub.Status,
		&exp, &sub.LastError, &sub.CreatedAt, &sub.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if exp.Valid {
		t := exp.Time
		sub.ExpiresAt = &t
	}
	return &sub, nil
}

func scanWebSubRows(rows *sql.Rows) (*WebSubSub, error) {
	var sub WebSubSub
	var exp sql.NullTime
	err := rows.Scan(&sub.ID, &sub.FeedID, &sub.Topic, &sub.Hub, &sub.Secret, &sub.LeaseSecs, &sub.Status,
		&exp, &sub.LastError, &sub.CreatedAt, &sub.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if exp.Valid {
		t := exp.Time
		sub.ExpiresAt = &t
	}
	return &sub, nil
}
