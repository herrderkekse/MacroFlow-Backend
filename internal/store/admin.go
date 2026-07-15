package store

import (
	"context"
	"errors"
)

// ErrUnknownUser is returned by admin operations that target a DB account that
// does not exist.
var ErrUnknownUser = errors.New("unknown user")

// UserStats summarises one user's stored change log.
type UserStats struct {
	UserID       string
	Bytes        int64
	Rows         int64
	Devices      int64
	LastChangeAt int64 // max updated_at over the user's changes (client clock, ms)
}

// AllUserStats returns per-user storage statistics for every user that has at
// least one stored change, keyed by user id.
func (s *Store) AllUserStats(ctx context.Context) (map[string]UserStats, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT user_id, `+usageSum+`, COUNT(*), COUNT(DISTINCT device_id), MAX(updated_at)
        FROM changes GROUP BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := map[string]UserStats{}
	for rows.Next() {
		var st UserStats
		if err := rows.Scan(&st.UserID, &st.Bytes, &st.Rows, &st.Devices, &st.LastChangeAt); err != nil {
			return nil, err
		}
		stats[st.UserID] = st
	}
	return stats, rows.Err()
}

// TotalUsage returns the logical byte size and row count of all stored changes
// across every user.
func (s *Store) TotalUsage(ctx context.Context) (bytes, rows int64, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT `+usageSum+`, COUNT(*) FROM changes`).Scan(&bytes, &rows)
	return bytes, rows, err
}

// Account is a self-service account row, without its password hash.
type Account struct {
	Username  string
	CreatedAt int64
}

// ListAccounts returns all self-service accounts, oldest first.
func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT username, created_at FROM users ORDER BY created_at, username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.Username, &a.CreatedAt); err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

// HasAccount reports whether a self-service account exists for username.
func (s *Store) HasAccount(ctx context.Context, username string) (bool, error) {
	_, ok, err := s.UserPasswordHash(ctx, username)
	return ok, err
}

// SetPassword replaces the bcrypt hash of an existing self-service account. It
// returns ErrUnknownUser if no such account exists.
func (s *Store) SetPassword(ctx context.Context, username, passwordHash string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE username = ?`, passwordHash, username)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrUnknownUser
	}
	return nil
}

// WipeUserData deletes every stored change for user and returns how many rows
// were removed. The user's seq counter is intentionally kept: reusing an
// already-handed-out seq would break clients holding a pull cursor, so future
// pushes must keep counting from where the log left off.
func (s *Store) WipeUserData(ctx context.Context, userID string) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	res, err := s.db.ExecContext(ctx, `DELETE FROM changes WHERE user_id = ?`, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteAccount removes a self-service account together with its change log
// and seq counter, atomically. It returns ErrUnknownUser if no such account
// exists (statically provisioned USERS entries are not accounts).
func (s *Store) DeleteAccount(ctx context.Context, username string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `DELETE FROM users WHERE username = ?`, username)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrUnknownUser
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM changes WHERE user_id = ?`, username); err != nil {
		return err
	}
	// The account is gone, so no client can hold a valid cursor for it any
	// more; dropping the counter lets a future account reuse the name cleanly.
	if _, err := tx.ExecContext(ctx, `DELETE FROM counters WHERE user_id = ?`, username); err != nil {
		return err
	}
	return tx.Commit()
}
