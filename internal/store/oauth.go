package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"

	"github.com/Quad4-Software/rss-discovery/internal/auth"
)

// OAuthClient is a confidential OAuth2 client.
type OAuthClient struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	ClientID     string     `json:"client_id"`
	Level        auth.Level `json:"level"`
	CreatedAt    time.Time  `json:"created_at"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	SecretHash   string     `json:"-"`
}

// CreateOAuthClient creates a client and returns plaintext secret once.
func (s *Store) CreateOAuthClient(ctx context.Context, name string, level auth.Level) (clientID, clientSecret string, rec *OAuthClient, err error) {
	var idBytes, secBytes [16]byte
	_, _ = rand.Read(idBytes[:])
	_, _ = rand.Read(secBytes[:])
	clientID = "rdc_" + hex.EncodeToString(idBytes[:])
	clientSecret = hex.EncodeToString(secBytes[:])
	sum := sha256.Sum256([]byte(clientSecret))
	hash := hex.EncodeToString(sum[:])
	id := newID()
	now := time.Now().UTC()
	rec = &OAuthClient{ID: id, Name: name, ClientID: clientID, Level: level, CreatedAt: now, SecretHash: hash}
	err = s.WithWrite(func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO oauth_clients (id, name, client_id, client_secret_hash, level, created_at)
			VALUES (?, ?, ?, ?, ?, ?)
		`, id, name, clientID, hash, string(level), now)
		return err
	})
	return clientID, clientSecret, rec, err
}

// LookupOAuthClient by client_id.
func (s *Store) LookupOAuthClient(ctx context.Context, clientID string) (*OAuthClient, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, client_id, client_secret_hash, level, created_at, revoked_at
		FROM oauth_clients WHERE client_id=?
	`, clientID)
	var c OAuthClient
	var level string
	var revoked sql.NullTime
	err := row.Scan(&c.ID, &c.Name, &c.ClientID, &c.SecretHash, &level, &c.CreatedAt, &revoked)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.Level = auth.Level(level)
	if revoked.Valid {
		t := revoked.Time
		c.RevokedAt = &t
	}
	return &c, nil
}

// ListOAuthClients returns clients without secrets.
func (s *Store) ListOAuthClients(ctx context.Context) ([]OAuthClient, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, client_id, level, created_at, revoked_at FROM oauth_clients ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OAuthClient
	for rows.Next() {
		var c OAuthClient
		var level string
		var revoked sql.NullTime
		if err := rows.Scan(&c.ID, &c.Name, &c.ClientID, &level, &c.CreatedAt, &revoked); err != nil {
			return nil, err
		}
		c.Level = auth.Level(level)
		if revoked.Valid {
			t := revoked.Time
			c.RevokedAt = &t
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RevokeOAuthClient marks client revoked and invalidates its access tokens.
func (s *Store) RevokeOAuthClient(ctx context.Context, id string) (bool, error) {
	var ok bool
	err := s.WithWrite(func(tx *sql.Tx) error {
		var clientID string
		if err := tx.QueryRowContext(ctx, `SELECT client_id FROM oauth_clients WHERE id=? AND revoked_at IS NULL`, id).Scan(&clientID); err != nil {
			if err == sql.ErrNoRows {
				return nil
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE oauth_clients SET revoked_at=now() WHERE id=?`, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE oauth_tokens SET revoked_at=now() WHERE client_id=? AND revoked_at IS NULL`, clientID)
		ok = true
		return err
	})
	return ok, err
}

// IssueOAuthToken stores a hashed access token.
func (s *Store) IssueOAuthToken(ctx context.Context, clientID string, level auth.Level, ttl time.Duration) (plain string, expires time.Time, err error) {
	var raw [24]byte
	_, _ = rand.Read(raw[:])
	plain = "rdo_" + hex.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(plain))
	hash := hex.EncodeToString(sum[:])
	expires = time.Now().UTC().Add(ttl)
	id := newID()
	err = s.WithWrite(func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO oauth_tokens (id, client_id, token_hash, level, expires_at, created_at)
			VALUES (?, ?, ?, ?, ?, now())
		`, id, clientID, hash, string(level), expires)
		return err
	})
	return plain, expires, err
}

// LookupOAuthToken validates a bearer access token from client_credentials.
func (s *Store) LookupOAuthToken(ctx context.Context, plain string) (*auth.TokenRecord, error) {
	hash := auth.HashToken(plain)
	row := s.db.QueryRowContext(ctx, `
		SELECT t.id, t.client_id, t.level, t.expires_at, t.revoked_at, c.revoked_at
		FROM oauth_tokens t
		JOIN oauth_clients c ON c.client_id = t.client_id
		WHERE t.token_hash=?
	`, hash)
	var id, clientID, level string
	var exp time.Time
	var tokRevoked, clientRevoked sql.NullTime
	err := row.Scan(&id, &clientID, &level, &exp, &tokRevoked, &clientRevoked)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if tokRevoked.Valid || clientRevoked.Valid || time.Now().UTC().After(exp) {
		return nil, nil
	}
	expCopy := exp
	return &auth.TokenRecord{
		ID:        id,
		Name:      "oauth:" + clientID,
		Level:     auth.Level(level),
		Hash:      hash,
		CreatedAt: time.Now().UTC(),
		ExpiresAt: &expCopy,
	}, nil
}
