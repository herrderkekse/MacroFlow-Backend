// Shares let one account hand a food/recipe/log payload to anyone with the
// resulting link, no account required on the receiving end. Tokens are opaque
// and unguessable; the store never interprets the payload JSON.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// ErrShareNotFound is returned by GetShare when the token is unknown or its
// share has expired.
var ErrShareNotFound = errors.New("share not found")

const sharesSchema = `
CREATE TABLE IF NOT EXISTS shares (
    token      TEXT    PRIMARY KEY,
    owner      TEXT    NOT NULL,
    kind       TEXT    NOT NULL,
    version    INTEGER NOT NULL,
    payload    TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
`

// Share is one stored share, as returned to the app.
type Share struct {
	Token     string          `json:"token"`
	Kind      string          `json:"kind"`
	Version   int             `json:"version"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt int64           `json:"createdAt"`
}

// CreateShare stores a payload under a fresh random token, expiring at
// now+ttl, and returns the token. It opportunistically deletes already-expired
// shares first, so the table never accumulates unbounded dead rows without
// needing a separate cleanup job.
func (s *Store) CreateShare(ctx context.Context, owner, kind string, version int, payload json.RawMessage, now time.Time, ttl time.Duration) (string, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	nowMs := now.UnixMilli()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM shares WHERE expires_at < ?`, nowMs); err != nil {
		return "", err
	}

	token, err := randomToken()
	if err != nil {
		return "", err
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO shares (token, owner, kind, version, payload, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		token, owner, kind, version, string(payload), nowMs, now.Add(ttl).UnixMilli())
	if err != nil {
		return "", err
	}
	return token, nil
}

// GetShare returns the share stored under token, or ErrShareNotFound if it
// doesn't exist or has expired.
func (s *Store) GetShare(ctx context.Context, token string, now time.Time) (*Share, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT kind, version, payload, created_at, expires_at FROM shares WHERE token = ?`, token)

	var (
		sh        Share
		payload   string
		expiresAt int64
	)
	switch err := row.Scan(&sh.Kind, &sh.Version, &payload, &sh.CreatedAt, &expiresAt); {
	case err == sql.ErrNoRows:
		return nil, ErrShareNotFound
	case err != nil:
		return nil, err
	}
	if expiresAt < now.UnixMilli() {
		return nil, ErrShareNotFound
	}
	sh.Token = token
	sh.Payload = json.RawMessage(payload)
	return &sh, nil
}

// randomToken returns a 32-character hex string from 16 bytes of CSPRNG
// output — large enough that guessing a live token is infeasible.
func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
