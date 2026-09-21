package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"strings"
	"unicode"
)

const embedDims = 64

// HashEmbed builds a fixed-dim hashed bag-of-words vector from text.
func HashEmbed(texts ...string) []float64 {
	vec := make([]float64, embedDims)
	for _, t := range texts {
		for _, tok := range tokenize(t) {
			h := fnv32(tok) % uint32(embedDims)
			sign := 1.0
			if fnv32(tok+"!")%2 == 0 {
				sign = -1
			}
			vec[h] += sign
		}
	}
	var sum float64
	for _, v := range vec {
		sum += v * v
	}
	norm := math.Sqrt(sum)
	if norm < 1e-12 {
		return vec
	}
	for i := range vec {
		vec[i] /= norm
	}
	return vec
}

func tokenize(s string) []string {
	s = strings.ToLower(s)
	var out []string
	var b strings.Builder
	flush := func() {
		if b.Len() >= 2 {
			out = append(out, b.String())
		}
		b.Reset()
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

func fnv32(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

func vecJSON(vec []float64) string {
	b, _ := json.Marshal(vec)
	return string(b)
}

func parseVec(s string) []float64 {
	var v []float64
	_ = json.Unmarshal([]byte(s), &v)
	return v
}

func cosine(a, b []float64) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	if n == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := 0; i < n; i++ {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	den := math.Sqrt(na) * math.Sqrt(nb)
	if den < 1e-12 {
		return 0
	}
	return dot / den
}

// UpsertFeedEmbedding stores vector for a feed as JSON.
func (s *Store) UpsertFeedEmbedding(ctx context.Context, feedID string, vec []float64) error {
	if len(vec) == 0 {
		vec = make([]float64, embedDims)
	}
	return s.WithWrite(func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO feed_embeddings (feed_id, dims, vector_json, updated_at)
			VALUES (?, ?, ?, now())
			ON CONFLICT (feed_id) DO UPDATE SET dims=excluded.dims, vector_json=excluded.vector_json, updated_at=now()
		`, feedID, len(vec), vecJSON(vec))
		return err
	})
}

// RecomputeFeedEmbedding from feed metadata.
func (s *Store) RecomputeFeedEmbedding(ctx context.Context, f Feed) error {
	vec := HashEmbed(f.Title, f.Description, f.Category, f.Blurb, f.Language)
	return s.UpsertFeedEmbedding(ctx, f.ID, vec)
}

// SimilarFeedsEmbedding finds feeds by cosine similarity of hashed embeddings.
func (s *Store) SimilarFeedsEmbedding(ctx context.Context, feedID string, limit int, minScore float64) ([]Feed, error) {
	src, err := s.GetFeed(ctx, feedID)
	if err != nil || src == nil {
		return nil, err
	}
	var raw string
	err = s.db.QueryRowContext(ctx, `SELECT vector_json FROM feed_embeddings WHERE feed_id=?`, feedID).Scan(&raw)
	if err != nil || raw == "" {
		_ = s.RecomputeFeedEmbedding(ctx, *src)
		_ = s.db.QueryRowContext(ctx, `SELECT vector_json FROM feed_embeddings WHERE feed_id=?`, feedID).Scan(&raw)
	}
	srcVec := parseVec(raw)
	if len(srcVec) == 0 {
		return s.SimilarFeeds(ctx, feedID, limit, minScore)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT e.feed_id, e.vector_json FROM feed_embeddings e WHERE e.feed_id <> ?
	`)
	if err != nil {
		return s.SimilarFeeds(ctx, feedID, limit, minScore)
	}
	defer rows.Close()
	type scored struct {
		id    string
		score float64
	}
	var ranked []scored
	for rows.Next() {
		var id, js string
		if err := rows.Scan(&id, &js); err != nil {
			continue
		}
		sc := cosine(srcVec, parseVec(js))
		if sc >= minScore {
			ranked = append(ranked, scored{id: id, score: sc})
		}
	}
	for i := 0; i < len(ranked); i++ {
		for j := i + 1; j < len(ranked); j++ {
			if ranked[j].score > ranked[i].score {
				ranked[i], ranked[j] = ranked[j], ranked[i]
			}
		}
	}
	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	var out []Feed
	for _, r := range ranked {
		f, err := s.GetFeed(ctx, r.id)
		if err != nil || f == nil {
			continue
		}
		out = append(out, *f)
	}
	if len(out) == 0 {
		return s.SimilarFeeds(ctx, feedID, limit, minScore)
	}
	return out, nil
}
