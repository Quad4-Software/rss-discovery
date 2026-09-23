package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Level is an API key privilege tier.
type Level string

const (
	LevelReadonly Level = "readonly"
	LevelStandard Level = "standard"
	LevelPriority Level = "priority"
	LevelAdmin    Level = "admin"
)

// Rank returns numeric privilege for comparisons.
func (l Level) Rank() int {
	switch l {
	case LevelReadonly:
		return 1
	case LevelStandard:
		return 2
	case LevelPriority:
		return 3
	case LevelAdmin:
		return 4
	default:
		return 0
	}
}

func ParseLevel(s string) (Level, error) {
	switch Level(strings.ToLower(strings.TrimSpace(s))) {
	case LevelReadonly, LevelStandard, LevelPriority, LevelAdmin:
		return Level(strings.ToLower(strings.TrimSpace(s))), nil
	default:
		return "", fmt.Errorf("invalid level %q (readonly|standard|priority|admin)", s)
	}
}

// RateLimit returns requests-per-minute and burst by level.
func RateLimit(level Level) (perMin int, burst int) {
	switch level {
	case LevelReadonly:
		return 600, 40
	case LevelStandard:
		return 3000, 120
	case LevelPriority:
		return 30000, 800
	case LevelAdmin:
		return 60000, 2000
	default:
		return 60, 10
	}
}

// AnonymousRateLimit is for unauthenticated traffic in public mode.
// Zero or negative values fall back to 60/min with burst 10.
func AnonymousRateLimit(perMin, burst int) (int, int) {
	if perMin < 1 {
		perMin = 60
	}
	if burst < 1 {
		burst = 10
	}
	return perMin, burst
}

// TokenRecord is a persisted API token metadata row (secret never stored plaintext).
type TokenRecord struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Level      Level      `json:"level"`
	Prefix     string     `json:"prefix"`
	Hash       string     `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	UseCount   int64      `json:"use_count"`
}

// AccessLog is an audited API access event.
type AccessLog struct {
	ID        string    `json:"id"`
	TokenID   string    `json:"token_id,omitempty"`
	Level     string    `json:"level,omitempty"`
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Status    int       `json:"status"`
	IP        string    `json:"ip"`
	UA        string    `json:"ua,omitempty"`
	LatencyMs int64     `json:"latency_ms"`
	CreatedAt time.Time `json:"created_at"`
}

// Generate creates a new plaintext token and its hash/prefix metadata.
// Format: rd_<level>_<32 hex bytes>
func Generate(level Level, name string, ttl time.Duration) (plaintext string, rec TokenRecord, err error) {
	if level.Rank() < 1 {
		return "", rec, fmt.Errorf("invalid level")
	}
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", rec, err
	}
	idRaw := sha256.Sum256(raw[:])
	id := hex.EncodeToString(idRaw[:8])
	secret := hex.EncodeToString(raw[:])
	plaintext = "rd_" + string(level) + "_" + secret
	prefix := plaintext[:min(12, len(plaintext))]
	sum := sha256.Sum256([]byte(plaintext))
	rec = TokenRecord{
		ID:        id,
		Name:      name,
		Level:     level,
		Prefix:    prefix,
		Hash:      hex.EncodeToString(sum[:]),
		CreatedAt: time.Now().UTC(),
	}
	if ttl > 0 {
		exp := rec.CreatedAt.Add(ttl)
		rec.ExpiresAt = &exp
	}
	return plaintext, rec, nil
}

// HashToken returns the sha256 hex of a plaintext token.
func HashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(plaintext)))
	return hex.EncodeToString(sum[:])
}

// EqualHash compares two hex digests in constant time.
func EqualHash(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Allows reports whether level may call method+path.
func Allows(level Level, method, path string) bool {
	if level == LevelAdmin {
		return true
	}
	path = strings.TrimSuffix(path, "/")
	if path == "" {
		path = "/"
	}
	switch {
	case path == "/" || path == "/healthz" || path == "/readyz" || path == "/livez" || path == "/openapi.json" || path == "/docs":
		return true
	case path == "/oauth/token" || path == "/v1/websub/callback":
		return true
	case strings.HasPrefix(path, "/v1/admin/"):
		return level.Rank() >= LevelAdmin.Rank()
	case path == "/v1/webhooks" || strings.HasPrefix(path, "/v1/webhooks/"):
		return level.Rank() >= LevelStandard.Rank()
	case path == "/v1/purge":
		return level.Rank() >= LevelAdmin.Rank()
	case strings.HasPrefix(path, "/v1/"):
		switch method {
		case "GET", "HEAD":
			return level.Rank() >= LevelReadonly.Rank()
		case "POST", "PUT", "PATCH", "DELETE":
			return level.Rank() >= LevelStandard.Rank()
		}
	}
	return false
}
