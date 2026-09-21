package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// SearchFeeds finds feeds by FTS or LIKE fallback.
func (s *Store) SearchFeeds(ctx context.Context, query string, limit int) ([]Feed, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return s.ListFeeds(ctx, limit, 0, "")
	}
	if s.ftsReady {
		rows, err := s.db.QueryContext(ctx, `
			SELECT f.id, f.url, COALESCE(f.site_url,''), COALESCE(f.title,''), COALESCE(f.description,''),
				COALESCE(f.category,''), COALESCE(f.blurb,''), COALESCE(f.format,''), COALESCE(f.language,''),
				COALESCE(f.favicon_url,''), f.last_status, COALESCE(f.last_error,''), f.last_fetched_at, f.last_ok_at,
				f.entry_count, f.fetch_count, f.fail_count, f.avg_latency_ms, COALESCE(f.source,'')
			FROM feeds f, (
				SELECT id, fts_main_feeds.match_bm25(id, ?) AS score
				FROM feeds
				WHERE score IS NOT NULL
				ORDER BY score DESC
				LIMIT ?
			) s
			WHERE f.id = s.id
			ORDER BY s.score DESC
		`, query, limit)
		if err == nil {
			defer rows.Close()
			out, scanErr := scanFeeds(rows)
			if scanErr == nil {
				return out, nil
			}
		}
	}
	like := "%" + query + "%"
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, url, COALESCE(site_url,''), COALESCE(title,''), COALESCE(description,''),
			COALESCE(category,''), COALESCE(blurb,''), COALESCE(format,''), COALESCE(language,''),
			COALESCE(favicon_url,''), last_status, COALESCE(last_error,''), last_fetched_at, last_ok_at,
			entry_count, fetch_count, fail_count, avg_latency_ms, COALESCE(source,'')
		FROM feeds
		WHERE title ILIKE ? OR description ILIKE ? OR blurb ILIKE ? OR url ILIKE ? OR category ILIKE ?
		ORDER BY title NULLS LAST
		LIMIT ?
	`, like, like, like, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFeeds(rows)
}

// SearchEntries finds entries by FTS or LIKE.
func (s *Store) SearchEntries(ctx context.Context, query string, limit int) ([]Entry, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query required")
	}
	if s.ftsReady {
		rows, err := s.db.QueryContext(ctx, `
			SELECT e.id, e.feed_id, COALESCE(e.guid,''), COALESCE(e.url,''), COALESCE(e.title,''),
				COALESCE(e.summary,''), COALESCE(e.author,''), e.published_at, e.updated_at,
				COALESCE(e.image_url,''), COALESCE(e.content_hash,'')
			FROM entries e, (
				SELECT id, fts_main_entries.match_bm25(id, ?) AS score
				FROM entries
				WHERE score IS NOT NULL
				ORDER BY score DESC
				LIMIT ?
			) s
			WHERE e.id = s.id
			ORDER BY s.score DESC
		`, query, limit)
		if err == nil {
			defer rows.Close()
			out, scanErr := scanEntryRows(rows)
			if scanErr == nil {
				return out, nil
			}
		}
	}
	like := "%" + query + "%"
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, feed_id, COALESCE(guid,''), COALESCE(url,''), COALESCE(title,''),
			COALESCE(summary,''), COALESCE(author,''), published_at, updated_at,
			COALESCE(image_url,''), COALESCE(content_hash,'')
		FROM entries
		WHERE title ILIKE ? OR summary ILIKE ? OR url ILIKE ?
		ORDER BY published_at DESC NULLS LAST
		LIMIT ?
	`, like, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEntryRows(rows)
}

// SearchFullText searches extracted article bodies.
func (s *Store) SearchFullText(ctx context.Context, query string, limit int) ([]FullText, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query required")
	}
	if s.ftsReady {
		rows, err := s.db.QueryContext(ctx, `
			SELECT ft.entry_id, COALESCE(ft.feed_id,''), COALESCE(ft.url,''), COALESCE(ft.title,''),
				COALESCE(ft.text,''), COALESCE(ft.excerpt,''), COALESCE(ft.byline,''), COALESCE(ft.site_name,''),
				COALESCE(ft.image_url,''), ft.storage, COALESCE(ft.blob_key,''), ft.byte_size
			FROM fulltext ft, (
				SELECT entry_id, fts_main_fulltext.match_bm25(entry_id, ?) AS score
				FROM fulltext
				WHERE score IS NOT NULL
				ORDER BY score DESC
				LIMIT ?
			) s
			WHERE ft.entry_id = s.entry_id
			ORDER BY s.score DESC
		`, query, limit)
		if err == nil {
			defer rows.Close()
			out, scanErr := scanFullTextRows(rows)
			if scanErr == nil {
				return out, nil
			}
		}
	}
	like := "%" + query + "%"
	rows, err := s.db.QueryContext(ctx, `
		SELECT entry_id, COALESCE(feed_id,''), COALESCE(url,''), COALESCE(title,''),
			COALESCE(text,''), COALESCE(excerpt,''), COALESCE(byline,''), COALESCE(site_name,''),
			COALESCE(image_url,''), storage, COALESCE(blob_key,''), byte_size
		FROM fulltext
		WHERE title ILIKE ? OR text ILIKE ? OR excerpt ILIKE ?
		LIMIT ?
	`, like, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFullTextRows(rows)
}

// SimilarFeeds finds related feeds by category and title token overlap.
func (s *Store) SimilarFeeds(ctx context.Context, feedID string, limit int, minScore float64) ([]Feed, error) {
	src, err := s.GetFeed(ctx, feedID)
	if err != nil || src == nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, url, COALESCE(site_url,''), COALESCE(title,''), COALESCE(description,''),
			COALESCE(category,''), COALESCE(blurb,''), COALESCE(format,''), COALESCE(language,''),
			COALESCE(favicon_url,''), last_status, COALESCE(last_error,''), last_fetched_at, last_ok_at,
			entry_count, fetch_count, fail_count, avg_latency_ms, COALESCE(source,''),
			jaccard(
				list_distinct(string_split(lower(COALESCE(title,'') || ' ' || COALESCE(category,'') || ' ' || COALESCE(blurb,'')), ' ')),
				list_distinct(string_split(?, ' '))
			) AS score
		FROM feeds
		WHERE id <> ?
		QUALIFY score IS NOT NULL AND score >= ?
		ORDER BY score DESC
		LIMIT ?
	`, strings.ToLower(src.Title+" "+src.Category+" "+src.Blurb), feedID, minScore, limit)
	if err != nil {
		return s.similarFallback(ctx, src, limit)
	}
	defer rows.Close()
	var out []Feed
	for rows.Next() {
		var f Feed
		var score float64
		var lastFetched, lastOK sql.NullTime
		if err := rows.Scan(&f.ID, &f.URL, &f.SiteURL, &f.Title, &f.Description, &f.Category, &f.Blurb,
			&f.Format, &f.Language, &f.FaviconURL, &f.LastStatus, &f.LastError, &lastFetched, &lastOK,
			&f.EntryCount, &f.FetchCount, &f.FailCount, &f.AvgLatencyMs, &f.Source, &score); err != nil {
			return s.similarFallback(ctx, src, limit)
		}
		if lastFetched.Valid {
			t := lastFetched.Time
			f.LastFetchedAt = &t
		}
		if lastOK.Valid {
			t := lastOK.Time
			f.LastOKAt = &t
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil || len(out) == 0 {
		return s.similarFallback(ctx, src, limit)
	}
	return out, nil
}

func (s *Store) similarFallback(ctx context.Context, src *Feed, limit int) ([]Feed, error) {
	word := firstWord(src.Title)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, url, COALESCE(site_url,''), COALESCE(title,''), COALESCE(description,''),
			COALESCE(category,''), COALESCE(blurb,''), COALESCE(format,''), COALESCE(language,''),
			COALESCE(favicon_url,''), last_status, COALESCE(last_error,''), last_fetched_at, last_ok_at,
			entry_count, fetch_count, fail_count, avg_latency_ms, COALESCE(source,'')
		FROM feeds
		WHERE id <> ? AND (
			(category <> '' AND category = ?) OR title ILIKE ?
		)
		LIMIT ?
	`, src.ID, src.Category, "%"+word+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFeeds(rows)
}

func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	if s == "" {
		return "feed"
	}
	return s
}

func scanEntryRows(rows *sql.Rows) ([]Entry, error) {
	var out []Entry
	for rows.Next() {
		var e Entry
		var pub, upd sql.NullTime
		if err := rows.Scan(&e.ID, &e.FeedID, &e.GUID, &e.URL, &e.Title, &e.Summary, &e.Author,
			&pub, &upd, &e.ImageURL, &e.ContentHash); err != nil {
			return nil, err
		}
		if pub.Valid {
			t := pub.Time
			e.PublishedAt = &t
		}
		if upd.Valid {
			t := upd.Time
			e.UpdatedAt = &t
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func scanFullTextRows(rows *sql.Rows) ([]FullText, error) {
	var out []FullText
	for rows.Next() {
		var ft FullText
		if err := rows.Scan(&ft.EntryID, &ft.FeedID, &ft.URL, &ft.Title, &ft.Text, &ft.Excerpt,
			&ft.Byline, &ft.SiteName, &ft.ImageURL, &ft.Storage, &ft.BlobKey, &ft.ByteSize); err != nil {
			return nil, err
		}
		out = append(out, ft)
	}
	return out, rows.Err()
}
