package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"
)

// Webhook is a signed callback destination.
type Webhook struct {
	ID         string     `json:"id"`
	URL        string     `json:"url"`
	Secret     string     `json:"-"`
	Events     string     `json:"events"`
	FeedID     string     `json:"feed_id,omitempty"`
	Active     bool       `json:"active"`
	FailCount  int        `json:"fail_count"`
	LastStatus int        `json:"last_status"`
	LastError  string     `json:"last_error,omitempty"`
	LastSentAt *time.Time `json:"last_sent_at,omitempty"`
	CreatedBy  string     `json:"created_by,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// CreateWebhook stores a webhook. Secret is returned once.
func (s *Store) CreateWebhook(ctx context.Context, url, events, feedID, createdBy string) (*Webhook, string, error) {
	var secretBytes [24]byte
	_, _ = rand.Read(secretBytes[:])
	secret := hex.EncodeToString(secretBytes[:])
	id := newID()
	now := time.Now().UTC()
	if events == "" {
		events = "feed.updated"
	}
	wh := &Webhook{
		ID: id, URL: url, Secret: secret, Events: events, FeedID: feedID,
		Active: true, CreatedBy: createdBy, CreatedAt: now,
	}
	err := s.WithWrite(func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO webhooks (id, url, secret, events, feed_id, active, created_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, true, ?, ?, ?)
		`, wh.ID, wh.URL, wh.Secret, wh.Events, nullStr(feedID), nullStr(createdBy), now, now)
		return err
	})
	return wh, secret, err
}

func scanWebhook(scanner interface {
	Scan(dest ...any) error
}) (*Webhook, error) {
	var w Webhook
	var lastSent sql.NullTime
	if err := scanner.Scan(&w.ID, &w.URL, &w.Secret, &w.Events, &w.FeedID, &w.Active, &w.FailCount,
		&w.LastStatus, &w.LastError, &lastSent, &w.CreatedBy, &w.CreatedAt); err != nil {
		return nil, err
	}
	if lastSent.Valid {
		t := lastSent.Time
		w.LastSentAt = &t
	}
	return &w, nil
}

const webhookSelectCols = `
	id, url, secret, events, COALESCE(feed_id,''), active, fail_count, last_status,
	COALESCE(last_error,''), last_sent_at, COALESCE(created_by,''), created_at
`

// ListWebhooks returns all webhooks (admin).
func (s *Store) ListWebhooks(ctx context.Context) ([]Webhook, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+webhookSelectCols+` FROM webhooks ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Webhook
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// ListWebhooksByCreator returns webhooks owned by createdBy.
func (s *Store) ListWebhooksByCreator(ctx context.Context, createdBy string) ([]Webhook, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+webhookSelectCols+`
		FROM webhooks WHERE created_by=? ORDER BY created_at DESC
	`, createdBy)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Webhook
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// GetWebhook by id.
func (s *Store) GetWebhook(ctx context.Context, id string) (*Webhook, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+webhookSelectCols+` FROM webhooks WHERE id=?`, id)
	w, err := scanWebhook(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return w, nil
}

// DeleteWebhook removes a webhook by id.
func (s *Store) DeleteWebhook(ctx context.Context, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM webhooks WHERE id=?`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteWebhookByCreator removes a webhook owned by createdBy.
func (s *Store) DeleteWebhookByCreator(ctx context.Context, id, createdBy string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM webhooks WHERE id=? AND created_by=?`, id, createdBy)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// MatchingWebhooks returns active webhooks for an event and optional feed.
func (s *Store) MatchingWebhooks(ctx context.Context, event, feedID string) ([]Webhook, error) {
	all, err := s.ListWebhooks(ctx)
	if err != nil {
		return nil, err
	}
	var out []Webhook
	for _, w := range all {
		if !w.Active {
			continue
		}
		if w.FeedID != "" && w.FeedID != feedID {
			continue
		}
		if !eventMatch(w.Events, event) {
			continue
		}
		out = append(out, w)
	}
	return out, nil
}

func eventMatch(events, event string) bool {
	for _, e := range strings.Split(events, ",") {
		e = strings.TrimSpace(e)
		if e == "*" || e == event {
			return true
		}
	}
	return false
}

// RecordWebhookDelivery updates webhook health and stores a delivery row.
func (s *Store) RecordWebhookDelivery(ctx context.Context, webhookID, event, payload string, status int, errMsg string) error {
	return s.WithWrite(func(tx *sql.Tx) error {
		id := newID()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO webhook_deliveries (id, webhook_id, event, payload, status, error, created_at)
			VALUES (?, ?, ?, ?, ?, ?, now())
		`, id, webhookID, event, payload, status, nullStr(errMsg)); err != nil {
			return err
		}
		ok := status >= 200 && status < 300
		if ok {
			_, err := tx.ExecContext(ctx, `
				UPDATE webhooks SET
					last_status=?, last_error='', last_sent_at=now(), fail_count=0, updated_at=now()
				WHERE id=?
			`, status, webhookID)
			return err
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE webhooks SET
				last_status=?,
				last_error=?,
				last_sent_at=now(),
				fail_count=fail_count+1,
				active=CASE WHEN fail_count+1 >= 20 THEN false ELSE active END,
				updated_at=now()
			WHERE id=?
		`, status, nullStr(errMsg), webhookID)
		return err
	})
}
