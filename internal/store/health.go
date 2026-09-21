package store

import (
	"context"
	"database/sql"
	"math"
	"time"
)

// FeedHealth is a scored view of fetch reliability.
type FeedHealth struct {
	FeedID        string     `json:"feed_id"`
	URL           string     `json:"url,omitempty"`
	Title         string     `json:"title,omitempty"`
	Score         float64    `json:"score"`
	FailStreak    int        `json:"fail_streak"`
	OKStreak      int        `json:"ok_streak"`
	LatencyP50Ms  float64    `json:"latency_p50_ms"`
	LatencyP99Ms  float64    `json:"latency_p99_ms"`
	AvgLatencyMs  float64    `json:"avg_latency_ms"`
	Format        string     `json:"format,omitempty"`
	FormatDrift   bool       `json:"format_drift"`
	LastStatus    int        `json:"last_status"`
	LastOKAt      *time.Time `json:"last_ok_at,omitempty"`
	LastFetchedAt *time.Time `json:"last_fetched_at,omitempty"`
	AgeSeconds    int64      `json:"age_seconds"`
	WithinSLO     bool       `json:"within_slo"`
	SLOSeconds    int64      `json:"slo_seconds"`
}

// FeedStatus is public freshness status for one feed.
type FeedStatus struct {
	FeedID        string     `json:"feed_id"`
	URL           string     `json:"url"`
	Title         string     `json:"title,omitempty"`
	LastStatus    int        `json:"last_status"`
	LastError     string     `json:"last_error,omitempty"`
	LastFetchedAt *time.Time `json:"last_fetched_at,omitempty"`
	LastOKAt      *time.Time `json:"last_ok_at,omitempty"`
	AgeSeconds    int64      `json:"age_seconds"`
	WithinSLO     bool       `json:"within_slo"`
	SLOSeconds    int64      `json:"slo_seconds"`
	WebSubStatus  string     `json:"websub_status,omitempty"`
	HealthScore   float64    `json:"health_score"`
}

// RecordLatencySample appends a latency sample and optional format observation.
func (s *Store) RecordLatencySample(ctx context.Context, feedID string, latencyMs int64, ok bool, format string) error {
	return s.WithWrite(func(tx *sql.Tx) error {
		id := newID()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO feed_latency_samples (id, feed_id, latency_ms, ok, created_at)
			VALUES (?, ?, ?, ?, now())
		`, id, feedID, latencyMs, ok); err != nil {
			return err
		}
		_, _ = tx.ExecContext(ctx, `
			DELETE FROM feed_latency_samples WHERE feed_id=? AND id NOT IN (
				SELECT id FROM feed_latency_samples WHERE feed_id=? ORDER BY created_at DESC LIMIT 200
			)
		`, feedID, feedID)
		if format != "" {
			var prev string
			_ = tx.QueryRowContext(ctx, `
				SELECT format FROM feed_format_history WHERE feed_id=? ORDER BY created_at DESC LIMIT 1
			`, feedID).Scan(&prev)
			if prev != format {
				_, _ = tx.ExecContext(ctx, `
					INSERT INTO feed_format_history (id, feed_id, format, created_at) VALUES (?, ?, ?, now())
				`, newID(), feedID, format)
			}
		}
		return nil
	})
}

// GetFeedHealth computes health for one feed.
func (s *Store) GetFeedHealth(ctx context.Context, feedID string, slo time.Duration) (*FeedHealth, error) {
	f, err := s.GetFeed(ctx, feedID)
	if err != nil || f == nil {
		return nil, err
	}
	h := &FeedHealth{
		FeedID: f.ID, URL: f.URL, Title: f.Title, AvgLatencyMs: f.AvgLatencyMs,
		LastStatus: f.LastStatus, LastOKAt: f.LastOKAt, LastFetchedAt: f.LastFetchedAt,
		Format: f.Format, SLOSeconds: int64(slo.Seconds()),
	}
	_ = s.db.QueryRowContext(ctx, `
		SELECT COALESCE(ok_streak,0), COALESCE(fail_streak,0) FROM feed_stats WHERE feed_id=?
	`, feedID).Scan(&h.OKStreak, &h.FailStreak)

	samples, _ := s.latencySamples(ctx, feedID, 100)
	h.LatencyP50Ms = percentile(samples, 0.50)
	h.LatencyP99Ms = percentile(samples, 0.99)

	var formats int
	_ = s.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT format) FROM feed_format_history WHERE feed_id=? AND created_at > (now() - INTERVAL '30 days')
	`, feedID).Scan(&formats)
	h.FormatDrift = formats > 1

	if f.LastOKAt != nil {
		h.AgeSeconds = int64(time.Since(*f.LastOKAt).Seconds())
	} else {
		h.AgeSeconds = 1 << 30
	}
	h.WithinSLO = f.LastOKAt != nil && time.Since(*f.LastOKAt) <= slo
	h.Score = computeHealthScore(h)
	return h, nil
}

// ListFeedHealth returns health for worst feeds first.
func (s *Store) ListFeedHealth(ctx context.Context, limit int, slo time.Duration) ([]FeedHealth, error) {
	feeds, err := s.ListFeeds(ctx, limit, 0, "")
	if err != nil {
		return nil, err
	}
	out := make([]FeedHealth, 0, len(feeds))
	for _, f := range feeds {
		h, err := s.GetFeedHealth(ctx, f.ID, slo)
		if err != nil || h == nil {
			continue
		}
		out = append(out, *h)
	}
	// Sort ascending by score (worst first) in Go.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Score < out[i].Score {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out, nil
}

// GetFeedStatus returns public freshness status.
func (s *Store) GetFeedStatus(ctx context.Context, feedID string, slo time.Duration) (*FeedStatus, error) {
	f, err := s.GetFeed(ctx, feedID)
	if err != nil || f == nil {
		return nil, err
	}
	h, _ := s.GetFeedHealth(ctx, feedID, slo)
	st := &FeedStatus{
		FeedID: f.ID, URL: f.URL, Title: f.Title, LastStatus: f.LastStatus, LastError: f.LastError,
		LastFetchedAt: f.LastFetchedAt, LastOKAt: f.LastOKAt, SLOSeconds: int64(slo.Seconds()),
	}
	if f.LastOKAt != nil {
		st.AgeSeconds = int64(time.Since(*f.LastOKAt).Seconds())
		st.WithinSLO = time.Since(*f.LastOKAt) <= slo
	}
	if h != nil {
		st.HealthScore = h.Score
	}
	if sub, _ := s.GetWebSubByFeed(ctx, feedID); sub != nil {
		st.WebSubStatus = sub.Status
	}
	return st, nil
}

// FreshnessSummary aggregates SLO compliance.
func (s *Store) FreshnessSummary(ctx context.Context, slo time.Duration) (map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, last_ok_at FROM feeds
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var total, within, never int
	for rows.Next() {
		var id string
		var okAt sql.NullTime
		if err := rows.Scan(&id, &okAt); err != nil {
			return nil, err
		}
		total++
		if !okAt.Valid {
			never++
			continue
		}
		if time.Since(okAt.Time) <= slo {
			within++
		}
	}
	pct := 0.0
	if total > 0 {
		pct = 100.0 * float64(within) / float64(total)
	}
	return map[string]any{
		"feeds":           total,
		"within_slo":      within,
		"never_ok":        never,
		"slo_seconds":     int64(slo.Seconds()),
		"compliance_pct":  math.Round(pct*100) / 100,
		"checked_at":      time.Now().UTC().Format(time.RFC3339),
	}, rows.Err()
}

func (s *Store) latencySamples(ctx context.Context, feedID string, limit int) ([]float64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT latency_ms FROM feed_latency_samples WHERE feed_id=? ORDER BY created_at DESC LIMIT ?
	`, feedID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []float64
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, float64(v))
	}
	return out, rows.Err()
}

func percentile(samples []float64, p float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	// Copy and sort ascending.
	cp := append([]float64(nil), samples...)
	for i := 0; i < len(cp); i++ {
		for j := i + 1; j < len(cp); j++ {
			if cp[j] < cp[i] {
				cp[i], cp[j] = cp[j], cp[i]
			}
		}
	}
	idx := int(math.Ceil(p*float64(len(cp)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return cp[idx]
}

func computeHealthScore(h *FeedHealth) float64 {
	score := 100.0
	score -= float64(h.FailStreak) * 8
	if h.LatencyP99Ms > 5000 {
		score -= 15
	} else if h.LatencyP99Ms > 2000 {
		score -= 8
	}
	if h.FormatDrift {
		score -= 10
	}
	if !h.WithinSLO {
		score -= 20
	}
	if h.LastStatus >= 400 {
		score -= 15
	}
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	return math.Round(score*10) / 10
}
