package store

import (
	"context"
)

// contactSchema holds messages submitted through the public contact form on
// the website. `type` distinguishes the submitting form (e.g. "contact",
// "support") so future forms can share the table. Messages are only ever read
// back via the admin API.
const contactSchema = `
CREATE TABLE IF NOT EXISTS contact_messages (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at INTEGER NOT NULL,
    type       TEXT    NOT NULL,
    name       TEXT    NOT NULL,
    email      TEXT    NOT NULL,
    subject    TEXT    NOT NULL,
    message    TEXT    NOT NULL
);
`

// ContactMessage is one stored contact-form submission.
type ContactMessage struct {
	ID        int64  `json:"id"`
	CreatedAt int64  `json:"createdAt"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	Subject   string `json:"subject"`
	Message   string `json:"message"`
}

// AddContactMessage stores a contact-form submission and returns its id. The
// caller validates the fields (including that kind is a known form type).
func (s *Store) AddContactMessage(ctx context.Context, kind, name, email, subject, message string, createdAt int64) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO contact_messages (created_at, type, name, email, subject, message) VALUES (?, ?, ?, ?, ?, ?)`,
		createdAt, kind, name, email, subject, message)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListContactMessages returns all stored submissions, newest first.
func (s *Store) ListContactMessages(ctx context.Context) ([]ContactMessage, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT id, created_at, type, name, email, subject, message
        FROM contact_messages
        ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	msgs := []ContactMessage{}
	for rows.Next() {
		var m ContactMessage
		if err := rows.Scan(&m.ID, &m.CreatedAt, &m.Type, &m.Name, &m.Email, &m.Subject, &m.Message); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// DeleteContactMessage removes one submission. It reports whether a row with
// that id existed.
func (s *Store) DeleteContactMessage(ctx context.Context, id int64) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	res, err := s.db.ExecContext(ctx, `DELETE FROM contact_messages WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
