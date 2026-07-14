package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// ErrUserExists is returned by CreateUser when the username is already taken.
var ErrUserExists = errors.New("user already exists")

// usersSchema holds self-service accounts created via the auth endpoints. It is
// separate from the static USERS config: those users are never written here.
const usersSchema = `
CREATE TABLE IF NOT EXISTS users (
    username      TEXT    PRIMARY KEY,
    password_hash TEXT    NOT NULL,
    created_at    INTEGER NOT NULL
);
`

// CreateUser inserts a new account with the given bcrypt password hash. It
// returns ErrUserExists if the username is already present. Usernames are
// stored verbatim; callers normalise/validate before calling.
func (s *Store) CreateUser(ctx context.Context, username, passwordHash string, createdAt int64) error {
	// Serialise with pushes so account creation and the seq counter never race
	// on the single SQLite writer.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, created_at) VALUES (?, ?, ?)`,
		username, passwordHash, createdAt)
	if err != nil {
		// modernc/sqlite surfaces a UNIQUE violation as an error string; the
		// PRIMARY KEY guarantees at most one row per username regardless.
		if isUniqueViolation(err) {
			return ErrUserExists
		}
		return err
	}
	return nil
}

// UserPasswordHash returns the stored bcrypt hash for username and whether the
// account exists.
func (s *Store) UserPasswordHash(ctx context.Context, username string) (hash string, ok bool, err error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT password_hash FROM users WHERE username = ?`, username)
	switch err := row.Scan(&hash); {
	case err == sql.ErrNoRows:
		return "", false, nil
	case err != nil:
		return "", false, err
	default:
		return hash, true, nil
	}
}

// isUniqueViolation reports whether err is a SQLite UNIQUE/PRIMARY KEY
// constraint failure. The pure-Go driver exposes it via message text.
func isUniqueViolation(err error) bool {
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
