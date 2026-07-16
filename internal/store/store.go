// Package store persists the per-user change log in SQLite.
//
// The server never interprets a change's data; it only stores an append-only,
// per-user, strictly-increasing log and hands it back in order. Because clients
// resolve conflicts per row (last-write-wins by updatedAt), the store keeps
// only the latest change per (user_id, table_name, row_id) — "compaction". A
// superseded version is deleted, but the newest is always retained and its seq
// is never reused.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"

	_ "modernc.org/sqlite"
)

// Change is one change to one row, as stored and served.
type Change struct {
	Seq       int64           `json:"seq"`
	DeviceID  string          `json:"deviceId"`
	Table     string          `json:"table"`
	RowID     string          `json:"rowId"`
	Op        string          `json:"op"`
	UpdatedAt int64           `json:"updatedAt"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// Incoming is a change as received from a client on push (no seq/user yet).
type Incoming struct {
	Table     string          `json:"table"`
	RowID     string          `json:"rowId"`
	Op        string          `json:"op"`
	UpdatedAt int64           `json:"updatedAt"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// Store owns the SQLite connection and serialises writes.
type Store struct {
	db *sql.DB
	// writeMu serialises the read-modify-write of the per-user seq counter so
	// concurrent pushes cannot assign the same seq. SQLite writes serialise
	// anyway; this keeps the counter logic simple and race-free in-process.
	writeMu sync.Mutex
}

const schema = `
CREATE TABLE IF NOT EXISTS counters (
    user_id  TEXT    PRIMARY KEY,
    next_seq INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS changes (
    user_id    TEXT    NOT NULL,
    seq        INTEGER NOT NULL,
    device_id  TEXT    NOT NULL,
    table_name TEXT    NOT NULL,
    row_id     TEXT    NOT NULL,
    op         TEXT    NOT NULL,
    updated_at INTEGER NOT NULL,
    data       TEXT,
    PRIMARY KEY (user_id, seq)
);

-- One live change per row: enforces compaction and makes upsert-by-key cheap.
CREATE UNIQUE INDEX IF NOT EXISTS idx_changes_key
    ON changes (user_id, table_name, row_id);
`

// Open opens (creating if needed) the SQLite database at path and applies the
// schema. WAL mode and a busy timeout keep concurrent readers happy while a
// push is committing.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(off)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(usersSchema); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(contactSchema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// Pull returns up to limit changes for user with seq > after, ordered by seq
// ascending. It also reports whether more changes exist beyond the page and the
// cursor the client should send next time (the last returned seq, or after when
// the page is empty).
func (s *Store) Pull(ctx context.Context, userID string, after int64, limit int) (changes []Change, nextCursor int64, hasMore bool, err error) {
	// Fetch one extra row to detect hasMore without a second query.
	rows, err := s.db.QueryContext(ctx, `
        SELECT seq, device_id, table_name, row_id, op, updated_at, data
        FROM changes
        WHERE user_id = ? AND seq > ?
        ORDER BY seq ASC
        LIMIT ?`, userID, after, limit+1)
	if err != nil {
		return nil, after, false, err
	}
	defer rows.Close()

	changes = make([]Change, 0, limit)
	for rows.Next() {
		if len(changes) == limit {
			hasMore = true
			break
		}
		var c Change
		var data sql.NullString
		if err := rows.Scan(&c.Seq, &c.DeviceID, &c.Table, &c.RowID, &c.Op, &c.UpdatedAt, &data); err != nil {
			return nil, after, false, err
		}
		if data.Valid {
			c.Data = json.RawMessage(data.String)
		}
		changes = append(changes, c)
	}
	if err := rows.Err(); err != nil {
		return nil, after, false, err
	}

	nextCursor = after
	if n := len(changes); n > 0 {
		nextCursor = changes[n-1].Seq
	}
	return changes, nextCursor, hasMore, nil
}

// Push appends a batch of changes for user in array order. Each change is
// assigned the next per-user seq. Compaction replaces any previous change for
// the same (table, row). The whole batch commits atomically — a malformed or
// failed batch stores nothing. It returns the count accepted and the highest
// seq assigned.
//
// When maxUserBytes > 0, the user's total stored size is measured after the
// batch is applied (so compaction and deletes are accounted for exactly); if it
// exceeds the cap the transaction is rolled back and ErrQuotaExceeded returned.
func (s *Store) Push(ctx context.Context, userID, deviceID string, changes []Incoming, maxUserBytes int64) (accepted int, maxSeq int64, err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	next, err := currentSeq(ctx, tx, userID)
	if err != nil {
		return 0, 0, err
	}

	upsert, err := tx.PrepareContext(ctx, `
        INSERT INTO changes (user_id, seq, device_id, table_name, row_id, op, updated_at, data)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT (user_id, table_name, row_id) DO UPDATE SET
            seq        = excluded.seq,
            device_id  = excluded.device_id,
            op         = excluded.op,
            updated_at = excluded.updated_at,
            data       = excluded.data`)
	if err != nil {
		return 0, 0, err
	}
	defer upsert.Close()

	for _, c := range changes {
		var data any
		if c.Op != "delete" && len(c.Data) > 0 {
			data = string(c.Data)
		}
		if _, err := upsert.ExecContext(ctx, userID, next, deviceID, c.Table, c.RowID, c.Op, c.UpdatedAt, data); err != nil {
			return 0, 0, err
		}
		maxSeq = next
		next++
	}

	// Enforce the per-user storage cap on the post-compaction footprint, so a
	// batch that only replaces or deletes rows can stay within (or free up)
	// quota rather than being rejected for its transient size.
	if maxUserBytes > 0 {
		bytes, _, err := queryUsage(ctx, tx, userID)
		if err != nil {
			return 0, 0, err
		}
		if bytes > maxUserBytes {
			return 0, 0, ErrQuotaExceeded
		}
	}

	if _, err := tx.ExecContext(ctx, `
        INSERT INTO counters (user_id, next_seq) VALUES (?, ?)
        ON CONFLICT (user_id) DO UPDATE SET next_seq = excluded.next_seq`,
		userID, next); err != nil {
		return 0, 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return len(changes), maxSeq, nil
}

// currentSeq returns the next seq to assign for user, defaulting to 1.
func currentSeq(ctx context.Context, tx *sql.Tx, userID string) (int64, error) {
	var next int64
	err := tx.QueryRowContext(ctx, `SELECT next_seq FROM counters WHERE user_id = ?`, userID).Scan(&next)
	switch {
	case err == sql.ErrNoRows:
		return 1, nil
	case err != nil:
		return 0, err
	default:
		return next, nil
	}
}
