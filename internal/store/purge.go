package store

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

// PurgeResult summarizes reclaim work.
type PurgeResult struct {
	EntriesDeleted   int64 `json:"entries_deleted"`
	FulltextDeleted  int64 `json:"fulltext_deleted"`
	FaviconsDeleted  int64 `json:"favicons_deleted"`
	BytesBefore      int64 `json:"bytes_before"`
	BytesAfter       int64 `json:"bytes_after"`
	ForcedByBudget   bool  `json:"forced_by_budget"`
}

// Purge removes expired rows and enforces the max storage budget.
func (s *Store) Purge(ctx context.Context, maxTotalBytes, maxFulltextBytes int64, entryTTL, fulltextTTL, faviconTTL time.Duration, batch int) (PurgeResult, error) {
	if batch < 100 {
		batch = 1000
	}
	res := PurgeResult{}
	before, _ := s.FileSize()
	res.BytesBefore = before

	err := s.WithWrite(func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `
			DELETE FROM entries WHERE fetched_at < (now() - (? * INTERVAL '1 hour'))
		`, int(entryTTL.Hours()))
		if err != nil {
			return err
		}
		res.EntriesDeleted, _ = r.RowsAffected()

		r, err = tx.ExecContext(ctx, `
			DELETE FROM fulltext WHERE accessed_at < (now() - (? * INTERVAL '1 hour'))
		`, int(fulltextTTL.Hours()))
		if err != nil {
			return err
		}
		res.FulltextDeleted, _ = r.RowsAffected()

		r, err = tx.ExecContext(ctx, `
			DELETE FROM favicons WHERE fetched_at < (now() - (? * INTERVAL '1 hour'))
		`, int(faviconTTL.Hours()))
		if err != nil {
			return err
		}
		res.FaviconsDeleted, _ = r.RowsAffected()

		var ftBytes int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(byte_size),0) FROM fulltext`).Scan(&ftBytes); err != nil {
			return err
		}
		for ftBytes > maxFulltextBytes {
			r, err := tx.ExecContext(ctx, `
				DELETE FROM fulltext WHERE entry_id IN (
					SELECT entry_id FROM fulltext ORDER BY accessed_at ASC NULLS FIRST LIMIT ?
				)
			`, batch)
			if err != nil {
				return err
			}
			n, _ := r.RowsAffected()
			res.FulltextDeleted += n
			res.ForcedByBudget = true
			if n == 0 {
				break
			}
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(byte_size),0) FROM fulltext`).Scan(&ftBytes); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return res, err
	}

	after, _ := s.FileSize()
	for after > maxTotalBytes {
		res.ForcedByBudget = true
		err := s.WithWrite(func(tx *sql.Tx) error {
			r, err := tx.ExecContext(ctx, `
				DELETE FROM fulltext WHERE entry_id IN (
					SELECT entry_id FROM fulltext ORDER BY accessed_at ASC NULLS FIRST LIMIT ?
				)
			`, batch)
			if err != nil {
				return err
			}
			n, _ := r.RowsAffected()
			res.FulltextDeleted += n
			if n == 0 {
				r, err = tx.ExecContext(ctx, `
					DELETE FROM entries WHERE id IN (
						SELECT id FROM entries ORDER BY fetched_at ASC NULLS FIRST LIMIT ?
					)
				`, batch)
				if err != nil {
					return err
				}
				n, _ = r.RowsAffected()
				res.EntriesDeleted += n
			}
			return nil
		})
		if err != nil {
			return res, err
		}
		_ = s.Checkpoint(ctx)
		after, _ = s.FileSize()
		if res.EntriesDeleted+res.FulltextDeleted == 0 {
			break
		}
	}
	res.BytesAfter = after
	slog.Info("purge complete",
		"entries", res.EntriesDeleted,
		"fulltext", res.FulltextDeleted,
		"favicons", res.FaviconsDeleted,
		"bytes_before", res.BytesBefore,
		"bytes_after", res.BytesAfter,
		"forced", res.ForcedByBudget,
	)
	return res, nil
}
