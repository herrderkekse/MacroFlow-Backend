package store

import (
	"context"
	"database/sql"
	"errors"
)

// ErrQuotaExceeded is returned by Push when a batch would leave the user over
// their configured storage cap. The batch stores nothing.
var ErrQuotaExceeded = errors.New("storage quota exceeded")

// usageSum is the SQL expression for a user's logical storage footprint: the
// byte length of each stored row's data plus its string columns. CAST(... AS
// BLOB) makes length() count bytes rather than characters; COALESCE(data,”)
// keeps delete tombstones (NULL data) from nulling the whole sum.
const usageSum = `COALESCE(SUM(
    length(CAST(COALESCE(data, '') AS BLOB))
    + length(table_name) + length(row_id) + length(device_id) + length(op)
), 0)`

// rowQuerier is satisfied by both *sql.DB and *sql.Tx, so usage can be measured
// against the committed database or inside an in-flight push transaction.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Usage returns the number of stored rows and their total logical byte size for
// user, from the committed database.
func (s *Store) Usage(ctx context.Context, userID string) (bytes, rows int64, err error) {
	return queryUsage(ctx, s.db, userID)
}

func queryUsage(ctx context.Context, q rowQuerier, userID string) (bytes, rows int64, err error) {
	err = q.QueryRowContext(ctx,
		`SELECT `+usageSum+`, COUNT(*) FROM changes WHERE user_id = ?`, userID).
		Scan(&bytes, &rows)
	return bytes, rows, err
}
