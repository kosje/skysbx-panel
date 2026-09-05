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
		return fmt.Errorf("create inbound %q: %w", in.Tag, err)
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

func (s *Store) UpdateInbound(in *Inbound) error {
	res, err := s.db.Exec(`UPDATE inbounds SET
		tag = ?, protocol = ?, port = ?, config = ?, client = ?, enabled = ?
		WHERE id = ?`,
		in.Tag, in.Protocol, in.Port, in.Config, in.Client, in.Enabled, in.ID)
	if err != nil {
		return fmt.Errorf("update inbound %d: %w", in.ID, err)
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
