package store

import (
	"database/sql"
	"fmt"
	"time"
)

const nodeCols = `id, name, token_hash, token_sha, address, country, enabled,
	last_seen_at, version, created_at`

func scanNode(sc interface{ Scan(...any) error }) (*Node, error) {
	var n Node
	var lastSeen sql.NullInt64
	var tokenSHA sql.NullString
	var created int64
	if err := sc.Scan(&n.ID, &n.Name, &n.TokenHash, &tokenSHA, &n.Address, &n.Country,
		&n.Enabled, &lastSeen, &n.Version, &created); err != nil {
		return nil, err
	}
	n.TokenSHA = tokenSHA.String
	if lastSeen.Valid {
		t := time.Unix(lastSeen.Int64, 0).UTC()
		n.LastSeenAt = &t
	}
	n.CreatedAt = time.Unix(created, 0).UTC()
	return &n, nil
}

func (s *Store) CreateNode(n *Node) error {
	res, err := s.db.Exec(`INSERT INTO nodes
		(name, token_hash, token_sha, address, country, enabled, version, created_at)
		VALUES (?, ?, ?, ?, ?, ?, '', unixepoch())`,
		n.Name, n.TokenHash, nullString(n.TokenSHA), n.Address, n.Country, n.Enabled)
	if err != nil {
		return asConflict(fmt.Errorf("create node %q: %w", n.Name, err))
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
		return asConflict(fmt.Errorf("update node %d: %w", n.ID, err))
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return ErrNotFound
	}
	return nil
}

// RotateNodeToken replaces the stored hash. The plaintext token is never
// persisted, so rotating is the only way to recover from a lost one.
func (s *Store) RotateNodeToken(id int64, tokenHash, tokenSHA string) error {
	res, err := s.db.Exec(`UPDATE nodes SET token_hash = ?, token_sha = ? WHERE id = ?`,
		tokenHash, nullString(tokenSHA), id)
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

// NodeByTokenSHA is the indexed path for authenticating a node: one lookup,
// rather than a bcrypt against every row. A token no node holds costs a single
// index probe — which is the whole point, because that endpoint is reachable
// without any credential at all.
func (s *Store) NodeByTokenSHA(sha string) (*Node, error) {
	if sha == "" {
		return nil, ErrNotFound
	}
	n, err := scanNode(s.db.QueryRow(
		`SELECT `+nodeCols+` FROM nodes WHERE token_sha = ?`, sha))
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return n, err
}

// SetNodeTokenSHA fills in the lookup key for a node that predates it, the
// first time that node authenticates. Until then its token exists only as a
// bcrypt hash, and a hash cannot be turned back into an index.
func (s *Store) SetNodeTokenSHA(id int64, sha string) error {
	_, err := s.db.Exec(
		`UPDATE nodes SET token_sha = ? WHERE id = ? AND token_sha IS NULL`, sha, id)
	return err
}

// NodesMissingTokenSHA counts the enabled nodes that still need the slow path.
// Once it reaches zero the scan is unreachable for every request, which is what
// closes the hole rather than merely narrowing it.
func (s *Store) NodesMissingTokenSHA() (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT count(*) FROM nodes WHERE enabled = 1 AND token_sha IS NULL`).Scan(&n)
	return n, err
}

// nullString keeps empty strings out of a UNIQUE index, where every one of them
// would collide with the next.
func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// MarkNodeSeen records a successful control-channel handshake.
func (s *Store) MarkNodeSeen(id int64, version string) error {
	_, err := s.db.Exec(
		`UPDATE nodes SET last_seen_at = unixepoch(), version = ? WHERE id = ?`, version, id)
	return err
}
