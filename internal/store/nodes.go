package store

import (
	"database/sql"
	"fmt"
	"time"
)

const nodeCols = `id, name, token_hash, address, country, enabled,
	last_seen_at, version, created_at`

func scanNode(sc interface{ Scan(...any) error }) (*Node, error) {
	var n Node
	var lastSeen sql.NullInt64
	var created int64
	if err := sc.Scan(&n.ID, &n.Name, &n.TokenHash, &n.Address, &n.Country,
		&n.Enabled, &lastSeen, &n.Version, &created); err != nil {
		return nil, err
	}
	if lastSeen.Valid {
		t := time.Unix(lastSeen.Int64, 0).UTC()
		n.LastSeenAt = &t
	}
	n.CreatedAt = time.Unix(created, 0).UTC()
	return &n, nil
}

func (s *Store) CreateNode(n *Node) error {
	res, err := s.db.Exec(`INSERT INTO nodes
		(name, token_hash, address, country, enabled, version, created_at)
		VALUES (?, ?, ?, ?, ?, '', unixepoch())`,
		n.Name, n.TokenHash, n.Address, n.Country, n.Enabled)
	if err != nil {
		return fmt.Errorf("create node %q: %w", n.Name, err)
	}
	n.ID, _ = res.LastInsertId()
	n.CreatedAt = time.Now().UTC()
	return nil
}

func (s *Store) Node(id int64) (*Node, error) {
	n, err := scanNode(s.db.QueryRow(`SELECT `+nodeCols+` FROM nodes WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return n, err
}

func (s *Store) Nodes() ([]*Node, error) {
	rows, err := s.db.Query(`SELECT ` + nodeCols + ` FROM nodes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// EnabledNodes is what the subscription generator iterates: a disabled node
// must not appear in anyone's subscription.
func (s *Store) EnabledNodes() ([]*Node, error) {
	rows, err := s.db.Query(`SELECT ` + nodeCols + ` FROM nodes WHERE enabled = 1 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) UpdateNode(n *Node) error {
	res, err := s.db.Exec(`UPDATE nodes SET
		name = ?, address = ?, country = ?, enabled = ? WHERE id = ?`,
		n.Name, n.Address, n.Country, n.Enabled, n.ID)
	if err != nil {
		return fmt.Errorf("update node %d: %w", n.ID, err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return ErrNotFound
	}
	return nil
}

// RotateNodeToken replaces the stored hash. The plaintext token is never
// persisted, so rotating is the only way to recover from a lost one.
func (s *Store) RotateNodeToken(id int64, tokenHash string) error {
	res, err := s.db.Exec(`UPDATE nodes SET token_hash = ? WHERE id = ?`, tokenHash, id)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteNode(id int64) error {
	res, err := s.db.Exec(`DELETE FROM nodes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkNodeSeen records a successful control-channel handshake.
func (s *Store) MarkNodeSeen(id int64, version string) error {
	_, err := s.db.Exec(
		`UPDATE nodes SET last_seen_at = unixepoch(), version = ? WHERE id = ?`, version, id)
	return err
}
