package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var ErrNotFound = errors.New("not found")

const userCols = `id, name, vless_uuid, password, ss_password, sub_token,
	enabled, expires_at, traffic_limit, traffic_used, note, created_at`

func scanUser(sc interface{ Scan(...any) error }) (*User, error) {
	var u User
	var expires sql.NullInt64
	var created int64
	if err := sc.Scan(&u.ID, &u.Name, &u.VlessUUID, &u.Password, &u.SSPassword,
		&u.SubToken, &u.Enabled, &expires, &u.TrafficLimit, &u.TrafficUsed,
		&u.Note, &created); err != nil {
		return nil, err
	}
	if expires.Valid {
		t := time.Unix(expires.Int64, 0).UTC()
		u.ExpiresAt = &t
	}
	u.CreatedAt = time.Unix(created, 0).UTC()
	return &u, nil
}

func (s *Store) CreateUser(u *User) error {
	var expires any
	if u.ExpiresAt != nil {
		expires = u.ExpiresAt.Unix()
	}
	res, err := s.db.Exec(`INSERT INTO users
		(name, vless_uuid, password, ss_password, sub_token, enabled,
		 expires_at, traffic_limit, traffic_used, note, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, unixepoch())`,
		u.Name, u.VlessUUID, u.Password, u.SSPassword, u.SubToken, u.Enabled,
		expires, u.TrafficLimit, u.Note)
	if err != nil {
		return fmt.Errorf("create user %q: %w", u.Name, err)
	}
	u.ID, _ = res.LastInsertId()
	u.CreatedAt = time.Now().UTC()
	return nil
}

func (s *Store) User(id int64) (*User, error) {
	u, err := scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *Store) UserBySubToken(token string) (*User, error) {
	u, err := scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE sub_token = ?`, token))
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *Store) Users() ([]*User, error) {
	rows, err := s.db.Query(`SELECT ` + userCols + ` FROM users ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateUser writes the mutable fields. traffic_used is deliberately not among
// them: it is owned by the traffic reporting path and must never be clobbered
// by an edit made in the UI.
func (s *Store) UpdateUser(u *User) error {
	var expires any
	if u.ExpiresAt != nil {
		expires = u.ExpiresAt.Unix()
	}
	res, err := s.db.Exec(`UPDATE users SET
		name = ?, vless_uuid = ?, password = ?, ss_password = ?,
		enabled = ?, expires_at = ?, traffic_limit = ?, note = ?
		WHERE id = ?`,
		u.Name, u.VlessUUID, u.Password, u.SSPassword,
		u.Enabled, expires, u.TrafficLimit, u.Note, u.ID)
	if err != nil {
		return fmt.Errorf("update user %d: %w", u.ID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteUser(id int64) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ResetUserTraffic(id int64) error {
	_, err := s.db.Exec(`UPDATE users SET traffic_used = 0 WHERE id = ?`, id)
	return err
}

// UserInboundIDs returns the inbounds a user is restricted to. An empty result
// means unrestricted — see the user_inbounds comment in the schema.
func (s *Store) UserInboundIDs(userID int64) ([]int64, error) {
	rows, err := s.db.Query(`SELECT inbound_id FROM user_inbounds WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) SetUserInbounds(userID int64, inboundIDs []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM user_inbounds WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, id := range inboundIDs {
		if _, err := tx.Exec(
			`INSERT INTO user_inbounds (user_id, inbound_id) VALUES (?, ?)`, userID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
