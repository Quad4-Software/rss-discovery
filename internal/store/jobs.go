package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
)

// Job statuses and types.
const (
	JobPending = "pending"
	JobRunning = "running"
	JobDone    = "done"
	JobFailed  = "failed"

	JobOPMLImport    = "opml_import"
	JobBulkDiscover  = "bulk_discover"
	ItemPending      = "pending"
	ItemRunning      = "running"
	ItemDone         = "done"
	ItemFailed       = "failed"
)

// Job is an async work unit.
type Job struct {
	ID          string     `json:"id"`
	Type        string     `json:"type"`
	Status      string     `json:"status"`
	Total       int        `json:"total"`
	DoneCount   int        `json:"done_count"`
	FailedCount int        `json:"failed_count"`
	Error       string     `json:"error,omitempty"`
	ResultJSON  string     `json:"result_json,omitempty"`
	CreatedBy   string     `json:"created_by,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// JobItem is one URL inside a job.
type JobItem struct {
	ID      string `json:"id"`
	JobID   string `json:"job_id"`
	URL     string `json:"url"`
	Status  string `json:"status"`
	FeedID  string `json:"feed_id,omitempty"`
	Error   string `json:"error,omitempty"`
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	sum := sha256.Sum256(b[:])
	return hex.EncodeToString(sum[:12])
}

// CreateJob inserts a job and optional URL items.
func (s *Store) CreateJob(ctx context.Context, typ, createdBy string, urls []string) (*Job, error) {
	id := newID()
	now := time.Now().UTC()
	j := &Job{
		ID: id, Type: typ, Status: JobPending, Total: len(urls),
		CreatedBy: createdBy, CreatedAt: now, UpdatedAt: now,
	}
	err := s.WithWrite(func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO async_jobs (id, type, status, total, created_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, j.ID, j.Type, j.Status, j.Total, nullStr(createdBy), j.CreatedAt, j.UpdatedAt)
		if err != nil {
			return err
		}
		for _, u := range urls {
			itemID := newID()
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO async_job_items (id, job_id, url, status, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?)
			`, itemID, j.ID, u, ItemPending, now, now); err != nil {
				return err
			}
		}
		return nil
	})
	return j, err
}

// GetJob returns a job by id.
func (s *Store) GetJob(ctx context.Context, id string) (*Job, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, type, status, total, done_count, failed_count, COALESCE(error,''), COALESCE(result_json,''),
			COALESCE(created_by,''), created_at, updated_at
		FROM async_jobs WHERE id=?
	`, id)
	var j Job
	err := row.Scan(&j.ID, &j.Type, &j.Status, &j.Total, &j.DoneCount, &j.FailedCount, &j.Error, &j.ResultJSON,
		&j.CreatedBy, &j.CreatedAt, &j.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &j, err
}

// ListJobs returns recent jobs.
func (s *Store) ListJobs(ctx context.Context, limit int) ([]Job, error) {
	if limit < 1 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, type, status, total, done_count, failed_count, COALESCE(error,''), COALESCE(result_json,''),
			COALESCE(created_by,''), created_at, updated_at
		FROM async_jobs ORDER BY created_at DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.Type, &j.Status, &j.Total, &j.DoneCount, &j.FailedCount, &j.Error, &j.ResultJSON,
			&j.CreatedBy, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// SetJobRunning marks job running.
func (s *Store) SetJobRunning(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE async_jobs SET status=?, updated_at=now() WHERE id=?`, JobRunning, id)
	return err
}

// CompleteJob finishes a job.
func (s *Store) CompleteJob(ctx context.Context, id, resultJSON string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE async_jobs SET status=?, result_json=?, updated_at=now() WHERE id=?
	`, JobDone, nullStr(resultJSON), id)
	return err
}

// FailJob marks job failed.
func (s *Store) FailJob(ctx context.Context, id, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE async_jobs SET status=?, error=?, updated_at=now() WHERE id=?
	`, JobFailed, nullStr(errMsg), id)
	return err
}

// ClaimPendingItems claims up to limit pending items for a job.
func (s *Store) ClaimPendingItems(ctx context.Context, jobID string, limit int) ([]JobItem, error) {
	var out []JobItem
	err := s.WithWrite(func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT id, job_id, url, status, COALESCE(feed_id,''), COALESCE(error,'')
			FROM async_job_items WHERE job_id=? AND status=? LIMIT ?
		`, jobID, ItemPending, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		var candidates []JobItem
		for rows.Next() {
			var it JobItem
			if err := rows.Scan(&it.ID, &it.JobID, &it.URL, &it.Status, &it.FeedID, &it.Error); err != nil {
				return err
			}
			candidates = append(candidates, it)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for i := range candidates {
			res, err := tx.ExecContext(ctx, `
				UPDATE async_job_items SET status=?, updated_at=now() WHERE id=? AND status=?
			`, ItemRunning, candidates[i].ID, ItemPending)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			if n != 1 {
				continue
			}
			candidates[i].Status = ItemRunning
			out = append(out, candidates[i])
		}
		return nil
	})
	return out, err
}

// MarkItemDone records success.
func (s *Store) MarkItemDone(ctx context.Context, itemID, feedID string) error {
	return s.WithWrite(func(tx *sql.Tx) error {
		var jobID string
		if err := tx.QueryRowContext(ctx, `SELECT job_id FROM async_job_items WHERE id=?`, itemID).Scan(&jobID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE async_job_items SET status=?, feed_id=?, updated_at=now() WHERE id=?
		`, ItemDone, nullStr(feedID), itemID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE async_jobs SET done_count=done_count+1, updated_at=now() WHERE id=?
		`, jobID)
		return err
	})
}

// MarkItemFailed records failure.
func (s *Store) MarkItemFailed(ctx context.Context, itemID, errMsg string) error {
	return s.WithWrite(func(tx *sql.Tx) error {
		var jobID string
		if err := tx.QueryRowContext(ctx, `SELECT job_id FROM async_job_items WHERE id=?`, itemID).Scan(&jobID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE async_job_items SET status=?, error=?, updated_at=now() WHERE id=?
		`, ItemFailed, nullStr(errMsg), itemID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE async_jobs SET failed_count=failed_count+1, updated_at=now() WHERE id=?
		`, jobID)
		return err
	})
}

// JobPendingRemaining returns pending+running count.
func (s *Store) JobPendingRemaining(ctx context.Context, jobID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM async_job_items WHERE job_id=? AND status IN (?, ?)
	`, jobID, ItemPending, ItemRunning).Scan(&n)
	return n, err
}

// ReclaimStaleJobItems resets running items older than age back to pending.
func (s *Store) ReclaimStaleJobItems(ctx context.Context, age time.Duration) (int64, error) {
	if age < time.Minute {
		age = 15 * time.Minute
	}
	secs := int(age.Seconds())
	var n int64
	err := s.WithWrite(func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE async_job_items SET status=?, updated_at=now()
			WHERE status=? AND updated_at < (now() - (? * INTERVAL '1 second'))
		`, ItemPending, ItemRunning, secs)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}

// ListJobsByCreator returns jobs for a token, or all when createdBy is empty (admin).
func (s *Store) ListJobsByCreator(ctx context.Context, createdBy string, limit int) ([]Job, error) {
	if limit < 1 {
		limit = 50
	}
	var rows *sql.Rows
	var err error
	if createdBy == "" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, type, status, total, done_count, failed_count, COALESCE(error,''), COALESCE(result_json,''),
				COALESCE(created_by,''), created_at, updated_at
			FROM async_jobs ORDER BY created_at DESC LIMIT ?
		`, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, type, status, total, done_count, failed_count, COALESCE(error,''), COALESCE(result_json,''),
				COALESCE(created_by,''), created_at, updated_at
			FROM async_jobs WHERE created_by=? ORDER BY created_at DESC LIMIT ?
		`, createdBy, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.Type, &j.Status, &j.Total, &j.DoneCount, &j.FailedCount, &j.Error, &j.ResultJSON,
			&j.CreatedBy, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ListJobItems returns items for a job.
func (s *Store) ListJobItems(ctx context.Context, jobID string, limit int) ([]JobItem, error) {
	if limit < 1 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, job_id, url, status, COALESCE(feed_id,''), COALESCE(error,'')
		FROM async_job_items WHERE job_id=? ORDER BY created_at LIMIT ?
	`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JobItem
	for rows.Next() {
		var it JobItem
		if err := rows.Scan(&it.ID, &it.JobID, &it.URL, &it.Status, &it.FeedID, &it.Error); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// EnsureJobExists helper for errors.
func EnsureJobExists(j *Job) error {
	if j == nil {
		return fmt.Errorf("job not found")
	}
	return nil
}
