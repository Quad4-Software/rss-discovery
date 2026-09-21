package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Feed is a catalog row.
type Feed struct {
	ID            string     `json:"id"`
	URL           string     `json:"url"`
	SiteURL       string     `json:"site_url,omitempty"`
	Title         string     `json:"title,omitempty"`
	Description   string     `json:"description,omitempty"`
	Category      string     `json:"category,omitempty"`
	Blurb         string     `json:"blurb,omitempty"`
	Format        string     `json:"format,omitempty"`
	Language      string     `json:"language,omitempty"`
	FaviconURL    string     `json:"favicon_url,omitempty"`
	LastStatus    int        `json:"last_status"`
	LastError     string     `json:"last_error,omitempty"`
	LastFetchedAt *time.Time `json:"last_fetched_at,omitempty"`
	LastOKAt      *time.Time `json:"last_ok_at,omitempty"`
	EntryCount    int        `json:"entry_count"`
	FetchCount    int        `json:"fetch_count"`
	FailCount     int        `json:"fail_count"`
	AvgLatencyMs  float64    `json:"avg_latency_ms"`
	Source        string     `json:"source,omitempty"`
}

// Entry is a feed item.
type Entry struct {
	ID          string     `json:"id"`
	FeedID      string     `json:"feed_id"`
	GUID        string     `json:"guid,omitempty"`
	URL         string     `json:"url,omitempty"`
	Title       string     `json:"title,omitempty"`
	Summary     string     `json:"summary,omitempty"`
	Author      string     `json:"author,omitempty"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
	ImageURL    string     `json:"image_url,omitempty"`
	ContentHash string     `json:"content_hash,omitempty"`
}

// FullText is extracted article content.
type FullText struct {
	EntryID   string `json:"entry_id"`
	FeedID    string `json:"feed_id,omitempty"`
	URL       string `json:"url,omitempty"`
	Title     string `json:"title,omitempty"`
	Text      string `json:"text,omitempty"`
	Excerpt   string `json:"excerpt,omitempty"`
	Byline    string `json:"byline,omitempty"`
	SiteName  string `json:"site_name,omitempty"`
	ImageURL  string `json:"image_url,omitempty"`
	Storage   string `json:"storage"`
	BlobKey   string `json:"blob_key,omitempty"`
	ByteSize  int    `json:"byte_size"`
}

func FeedID(url string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(url)))
	return hex.EncodeToString(sum[:16])
}

func EntryID(feedID, guid, url string) string {
	key := feedID + "|" + guid
	if guid == "" {
		key = feedID + "|" + url
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:16])
}

// UpsertFeed inserts or updates a feed catalog row.
func (s *Store) UpsertFeed(ctx context.Context, f Feed) error {
	if f.ID == "" {
		f.ID = FeedID(f.URL)
	}
	return s.WithWrite(func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO feeds (
				id, url, site_url, title, description, category, blurb, format, language,
				favicon_url, source, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, now())
			ON CONFLICT (id) DO UPDATE SET
				site_url=COALESCE(excluded.site_url, feeds.site_url),
				title=COALESCE(NULLIF(excluded.title,''), feeds.title),
				description=COALESCE(NULLIF(excluded.description,''), feeds.description),
				category=COALESCE(NULLIF(excluded.category,''), feeds.category),
				blurb=COALESCE(NULLIF(excluded.blurb,''), feeds.blurb),
				format=COALESCE(NULLIF(excluded.format,''), feeds.format),
				language=COALESCE(NULLIF(excluded.language,''), feeds.language),
				favicon_url=COALESCE(NULLIF(excluded.favicon_url,''), feeds.favicon_url),
				source=COALESCE(NULLIF(excluded.source,''), feeds.source),
				updated_at=now()
		`, f.ID, f.URL, nullStr(f.SiteURL), nullStr(f.Title), nullStr(f.Description),
			nullStr(f.Category), nullStr(f.Blurb), nullStr(f.Format), nullStr(f.Language),
			nullStr(f.FaviconURL), nullStr(f.Source))
		return err
	})
}

// UpsertFeedsBatch bulk-inserts seed feeds.
func (s *Store) UpsertFeedsBatch(ctx context.Context, feeds []Feed) (int, error) {
	n := 0
	err := s.WithWrite(func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO feeds (id, url, site_url, title, description, category, blurb, source, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, now())
			ON CONFLICT (id) DO UPDATE SET
				site_url=COALESCE(excluded.site_url, feeds.site_url),
				title=COALESCE(NULLIF(excluded.title,''), feeds.title),
				category=COALESCE(NULLIF(excluded.category,''), feeds.category),
				blurb=COALESCE(NULLIF(excluded.blurb,''), feeds.blurb),
				source=COALESCE(NULLIF(excluded.source,''), feeds.source),
				updated_at=now()
		`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, f := range feeds {
			if f.URL == "" {
				continue
			}
			if f.ID == "" {
				f.ID = FeedID(f.URL)
			}
			if _, err := stmt.ExecContext(ctx, f.ID, f.URL, nullStr(f.SiteURL), nullStr(f.Title),
				nullStr(f.Description), nullStr(f.Category), nullStr(f.Blurb), nullStr(f.Source)); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// RecordFetch updates fetch health stats for a feed.
func (s *Store) RecordFetch(ctx context.Context, feedID string, status int, latencyMs int64, errMsg string, etag, lastMod string, ok bool) error {
	return s.WithWrite(func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE feeds SET
				last_status=?,
				last_error=?,
				last_fetched_at=now(),
				last_ok_at=CASE WHEN ? THEN now() ELSE last_ok_at END,
				fetch_count=fetch_count+1,
				fail_count=fail_count+CASE WHEN ? THEN 0 ELSE 1 END,
				avg_latency_ms=CASE WHEN fetch_count=0 THEN ? ELSE (avg_latency_ms*fetch_count+?)/(fetch_count+1) END,
				etag=COALESCE(NULLIF(?, ''), etag),
				last_modified=COALESCE(NULLIF(?, ''), last_modified),
				updated_at=now()
			WHERE id=?
		`, status, nullStr(errMsg), ok, ok, float64(latencyMs), float64(latencyMs), etag, lastMod, feedID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO feed_stats (feed_id, latency_ms_last, ok_streak, fail_streak, updated_at)
			VALUES (?, ?, CASE WHEN ? THEN 1 ELSE 0 END, CASE WHEN ? THEN 0 ELSE 1 END, now())
			ON CONFLICT (feed_id) DO UPDATE SET
				latency_ms_last=excluded.latency_ms_last,
				ok_streak=CASE WHEN ? THEN feed_stats.ok_streak+1 ELSE 0 END,
				fail_streak=CASE WHEN ? THEN 0 ELSE feed_stats.fail_streak+1 END,
				updated_at=now()
		`, feedID, latencyMs, ok, ok, ok, ok)
		return err
	})
}

// ReplaceEntries writes entries for a feed and trims to maxPerFeed.
func (s *Store) ReplaceEntries(ctx context.Context, feedID string, entries []Entry, maxPerFeed int) error {
	return s.WithWrite(func(tx *sql.Tx) error {
		for _, e := range entries {
			if e.ID == "" {
				e.ID = EntryID(feedID, e.GUID, e.URL)
			}
			e.FeedID = feedID
			_, err := tx.ExecContext(ctx, `
				INSERT INTO entries (
					id, feed_id, guid, url, title, summary, author, published_at, updated_at, image_url, content_hash, fetched_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, now())
				ON CONFLICT (id) DO UPDATE SET
					title=excluded.title,
					summary=excluded.summary,
					author=excluded.author,
					published_at=COALESCE(excluded.published_at, entries.published_at),
					updated_at=COALESCE(excluded.updated_at, entries.updated_at),
					image_url=COALESCE(excluded.image_url, entries.image_url),
					content_hash=excluded.content_hash,
					fetched_at=now()
			`, e.ID, feedID, nullStr(e.GUID), nullStr(e.URL), nullStr(e.Title), nullStr(e.Summary),
				nullStr(e.Author), e.PublishedAt, e.UpdatedAt, nullStr(e.ImageURL), nullStr(e.ContentHash))
			if err != nil {
				return err
			}
		}
		if maxPerFeed > 0 {
			_, err := tx.ExecContext(ctx, `
				DELETE FROM entries WHERE feed_id=? AND id NOT IN (
					SELECT id FROM entries WHERE feed_id=? ORDER BY published_at DESC NULLS LAST, fetched_at DESC LIMIT ?
				)
			`, feedID, feedID, maxPerFeed)
			if err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE feeds SET entry_count=(SELECT COUNT(*) FROM entries WHERE feed_id=?), updated_at=now() WHERE id=?
		`, feedID, feedID)
		return err
	})
}

// UpsertFullText stores extracted article text metadata (and inline text when local).
func (s *Store) UpsertFullText(ctx context.Context, ft FullText) error {
	return s.WithWrite(func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO fulltext (
				entry_id, feed_id, url, title, text, excerpt, byline, site_name, image_url,
				storage, blob_key, byte_size, fetched_at, accessed_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, now(), now())
			ON CONFLICT (entry_id) DO UPDATE SET
				title=excluded.title,
				text=excluded.text,
				excerpt=excluded.excerpt,
				byline=excluded.byline,
				site_name=excluded.site_name,
				image_url=excluded.image_url,
				storage=excluded.storage,
				blob_key=excluded.blob_key,
				byte_size=excluded.byte_size,
				fetched_at=now(),
				accessed_at=now()
		`, ft.EntryID, nullStr(ft.FeedID), nullStr(ft.URL), nullStr(ft.Title), nullStr(ft.Text),
			nullStr(ft.Excerpt), nullStr(ft.Byline), nullStr(ft.SiteName), nullStr(ft.ImageURL),
			ft.Storage, nullStr(ft.BlobKey), ft.ByteSize)
		return err
	})
}

// TouchFullText updates LRU access time.
func (s *Store) TouchFullText(ctx context.Context, entryID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE fulltext SET accessed_at=now() WHERE entry_id=?`, entryID)
	return err
}

// SaveFavicon stores favicon bytes for a feed.
func (s *Store) SaveFavicon(ctx context.Context, feedID, url, contentType string, data []byte) error {
	return s.WithWrite(func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO favicons (feed_id, url, content_type, data, byte_size, fetched_at)
			VALUES (?, ?, ?, ?, ?, now())
			ON CONFLICT (feed_id) DO UPDATE SET
				url=excluded.url,
				content_type=excluded.content_type,
				data=excluded.data,
				byte_size=excluded.byte_size,
				fetched_at=now()
		`, feedID, url, contentType, data, len(data))
		return err
	})
}

// GetFeed by id or url.
func (s *Store) GetFeed(ctx context.Context, idOrURL string) (*Feed, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, url, COALESCE(site_url,''), COALESCE(title,''), COALESCE(description,''),
			COALESCE(category,''), COALESCE(blurb,''), COALESCE(format,''), COALESCE(language,''),
			COALESCE(favicon_url,''), last_status, COALESCE(last_error,''), last_fetched_at, last_ok_at,
			entry_count, fetch_count, fail_count, avg_latency_ms, COALESCE(source,'')
		FROM feeds WHERE id=? OR url=?
	`, idOrURL, idOrURL)
	return scanFeed(row)
}

// ListFeedsDue returns feeds that need a refresh.
func (s *Store) ListFeedsDue(ctx context.Context, limit int, maxAge time.Duration) ([]Feed, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, url, COALESCE(site_url,''), COALESCE(title,''), COALESCE(description,''),
			COALESCE(category,''), COALESCE(blurb,''), COALESCE(format,''), COALESCE(language,''),
			COALESCE(favicon_url,''), last_status, COALESCE(last_error,''), last_fetched_at, last_ok_at,
			entry_count, fetch_count, fail_count, avg_latency_ms, COALESCE(source,'')
		FROM feeds
		WHERE last_fetched_at IS NULL OR last_fetched_at < (now() - (? * INTERVAL '1 hour'))
		ORDER BY COALESCE(last_fetched_at, TIMESTAMP '1970-01-01') ASC
		LIMIT ?
	`, int(maxAge.Hours()), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFeeds(rows)
}

// ListFeeds returns a page of feeds.
func (s *Store) ListFeeds(ctx context.Context, limit, offset int, category string) ([]Feed, error) {
	q := `
		SELECT id, url, COALESCE(site_url,''), COALESCE(title,''), COALESCE(description,''),
			COALESCE(category,''), COALESCE(blurb,''), COALESCE(format,''), COALESCE(language,''),
			COALESCE(favicon_url,''), last_status, COALESCE(last_error,''), last_fetched_at, last_ok_at,
			entry_count, fetch_count, fail_count, avg_latency_ms, COALESCE(source,'')
		FROM feeds`
	args := []any{}
	if category != "" {
		q += ` WHERE category=?`
		args = append(args, category)
	}
	q += ` ORDER BY title NULLS LAST, url LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFeeds(rows)
}

func scanFeed(row *sql.Row) (*Feed, error) {
	var f Feed
	var lastFetched, lastOK sql.NullTime
	err := row.Scan(&f.ID, &f.URL, &f.SiteURL, &f.Title, &f.Description, &f.Category, &f.Blurb,
		&f.Format, &f.Language, &f.FaviconURL, &f.LastStatus, &f.LastError, &lastFetched, &lastOK,
		&f.EntryCount, &f.FetchCount, &f.FailCount, &f.AvgLatencyMs, &f.Source)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if lastFetched.Valid {
		t := lastFetched.Time
		f.LastFetchedAt = &t
	}
	if lastOK.Valid {
		t := lastOK.Time
		f.LastOKAt = &t
	}
	return &f, nil
}

func scanFeeds(rows *sql.Rows) ([]Feed, error) {
	var out []Feed
	for rows.Next() {
		var f Feed
		var lastFetched, lastOK sql.NullTime
		if err := rows.Scan(&f.ID, &f.URL, &f.SiteURL, &f.Title, &f.Description, &f.Category, &f.Blurb,
			&f.Format, &f.Language, &f.FaviconURL, &f.LastStatus, &f.LastError, &lastFetched, &lastOK,
			&f.EntryCount, &f.FetchCount, &f.FailCount, &f.AvgLatencyMs, &f.Source); err != nil {
			return nil, err
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
	return out, rows.Err()
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// GetEntry returns an entry by id.
func (s *Store) GetEntry(ctx context.Context, id string) (*Entry, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, feed_id, COALESCE(guid,''), COALESCE(url,''), COALESCE(title,''), COALESCE(summary,''),
			COALESCE(author,''), published_at, updated_at, COALESCE(image_url,''), COALESCE(content_hash,'')
		FROM entries WHERE id=?
	`, id)
	var e Entry
	var pub, upd sql.NullTime
	err := row.Scan(&e.ID, &e.FeedID, &e.GUID, &e.URL, &e.Title, &e.Summary, &e.Author, &pub, &upd, &e.ImageURL, &e.ContentHash)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
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
	return &e, nil
}

// GetFullText returns cached full text for an entry.
func (s *Store) GetFullText(ctx context.Context, entryID string) (*FullText, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT entry_id, COALESCE(feed_id,''), COALESCE(url,''), COALESCE(title,''), COALESCE(text,''),
			COALESCE(excerpt,''), COALESCE(byline,''), COALESCE(site_name,''), COALESCE(image_url,''),
			storage, COALESCE(blob_key,''), byte_size
		FROM fulltext WHERE entry_id=?
	`, entryID)
	var ft FullText
	err := row.Scan(&ft.EntryID, &ft.FeedID, &ft.URL, &ft.Title, &ft.Text, &ft.Excerpt, &ft.Byline,
		&ft.SiteName, &ft.ImageURL, &ft.Storage, &ft.BlobKey, &ft.ByteSize)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = s.TouchFullText(ctx, entryID)
	return &ft, nil
}

// CountFeeds returns total catalog size.
func (s *Store) CountFeeds(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM feeds`).Scan(&n)
	return n, err
}

func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	return s.WithWrite(func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO meta(key, value) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET value=excluded.value
		`, key, value)
		return err
	})
}

func (s *Store) GetMeta(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *Store) String() string {
	return fmt.Sprintf("duckdb(%s)", s.connStr)
}
