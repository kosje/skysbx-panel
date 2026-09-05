package store

import (
	"database/sql"
	"fmt"
)

const inboundCols = `id, node_id, tag, protocol, port, config, client, enabled`

func scanInbound(sc interface{ Scan(...any) error }) (*Inbound, error) {
	var in Inbound
	if err := sc.Scan(&in.ID, &in.NodeID, &in.Tag, &in.Protocol, &in.Port,
		&in.Config, &in.Client, &in.Enabled); err != nil {
		return nil, err
	}
	return &in, nil
}

func (s *Store) CreateInbound(in *Inbound) error {
	res, err := s.db.Exec(`INSERT INTO inbounds
		(node_id, tag, protocol, port, config, client, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		in.NodeID, in.Tag, in.Protocol, in.Port, in.Config, in.Client, in.Enabled)
	if err != nil {
		return asConflict(fmt.Errorf("create inbound %q: %w", in.Tag, err))
	}
	in.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) Inbound(id int64) (*Inbound, error) {
	in, err := scanInbound(s.db.QueryRow(`SELECT `+inboundCols+` FROM inbounds WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return in, err
}

func (s *Store) InboundByTag(tag string) (*Inbound, error) {
	in, err := scanInbound(s.db.QueryRow(`SELECT `+inboundCols+` FROM inbounds WHERE tag = ?`, tag))
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return in, err
}

func (s *Store) Inbounds() ([]*Inbound, error) {
	return s.queryInbounds(`SELECT ` + inboundCols + ` FROM inbounds ORDER BY node_id, tag`)
}

// NodeInbounds returns every inbound configured for a node, enabled or not.
// The hub filters by enabled itself, because a disabled inbound must still be
// removed from the node's running config rather than silently left behind.
func (s *Store) NodeInbounds(nodeID int64) ([]*Inbound, error) {
	return s.queryInbounds(
		`SELECT `+inboundCols+` FROM inbounds WHERE node_id = ? ORDER BY tag`, nodeID)
}

func (s *Store) EnabledInbounds() ([]*Inbound, error) {
	return s.queryInbounds(
		`SELECT ` + inboundCols + ` FROM inbounds WHERE enabled = 1 ORDER BY node_id, tag`)
}

func (s *Store) queryInbounds(q string, args ...any) ([]*Inbound, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Inbound
	for rows.Next() {
		in, err := scanInbound(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// Retag is one inbound's new tag and the sing-box object carrying it. The tag
// lives in both, and updating one without the other leaves the node listening
// under a name no user list mentions.
type Retag struct {
	ID     int64
	Tag    string
	Config string
}

// RetagInbounds renames a set of inbounds at once.
//
// Two phases, because tags are globally unique and a rename can shuffle them
// between rows: an inbound taking a tag its neighbour still holds would trip
// the unique index even though the end state is perfectly valid. Everything
// moves to a placeholder first. Both phases and the rollback are one
// transaction, so a failure leaves every tag as it was.
func (s *Store) RetagInbounds(updates []Retag) error {
	if len(updates) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, u := range updates {
		if _, err := tx.Exec(`UPDATE inbounds SET tag = ? WHERE id = ?`,
			fmt.Sprintf("retag-%d", u.ID), u.ID); err != nil {
			return asConflict(fmt.Errorf("retag inbound %d: %w", u.ID, err))
		}
	}
	for _, u := range updates {
		res, err := tx.Exec(`UPDATE inbounds SET tag = ?, config = ? WHERE id = ?`,
			u.Tag, u.Config, u.ID)
		if err != nil {
			return asConflict(fmt.Errorf("retag inbound %d to %s: %w", u.ID, u.Tag, err))
		}
		if rows, _ := res.RowsAffected(); rows == 0 {
			return ErrNotFound
		}
	}
	return tx.Commit()
}

// NodeInboundsByID is NodeInbounds in creation order rather than tag order.
// Deriving tags from a node name has to be deterministic, and ordering by the
// thing being rewritten is not.
func (s *Store) NodeInboundsByID(nodeID int64) ([]*Inbound, error) {
	return s.queryInbounds(
		`SELECT `+inboundCols+` FROM inbounds WHERE node_id = ? ORDER BY id`, nodeID)
}

func (s *Store) UpdateInbound(in *Inbound) error {
	res, err := s.db.Exec(`UPDATE inbounds SET
		tag = ?, protocol = ?, port = ?, config = ?, client = ?, enabled = ?
		WHERE id = ?`,
		in.Tag, in.Protocol, in.Port, in.Config, in.Client, in.Enabled, in.ID)
	if err != nil {
		return asConflict(fmt.Errorf("update inbound %d: %w", in.ID, err))
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteInbound(id int64) error {
	res, err := s.db.Exec(`DELETE FROM inbounds WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return ErrNotFound
	}
	return nil
}
