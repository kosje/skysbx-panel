package store

import "fmt"

// ActivitySample is one node's view of one user at one moment.
type ActivitySample struct {
	UserID int64
	Conns  int
	Peers  int
	Ports  int
	IPs    int
}

// ActivityRow is an hour of it.
type ActivityRow struct {
	Hour    int64
	NodeID  int64
	Conns   int
	Peers   int
	Ports   int
	IPs     int
	Samples int
}

// RecordActivity folds a batch of samples into the hour they belong to.
//
// The stored figure is the peak, not the average. An account that opens two
// hundred connections for ten minutes an hour averages to nothing, and the ten
// minutes are exactly what an operator is looking for.
func (s *Store) RecordActivity(nodeID, hour int64, samples []ActivitySample) error {
	if len(samples) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, a := range samples {
		if _, err := tx.Exec(`
			INSERT INTO user_activity (user_id, node_id, hour, conns, peers, ports, ips, samples)
			VALUES (?, ?, ?, ?, ?, ?, ?, 1)
			ON CONFLICT(user_id, node_id, hour) DO UPDATE SET
				conns   = max(conns,  excluded.conns),
				peers   = max(peers,  excluded.peers),
				ports   = max(ports,  excluded.ports),
				ips     = max(ips,    excluded.ips),
				samples = samples + 1`,
			a.UserID, nodeID, hour, a.Conns, a.Peers, a.Ports, a.IPs); err != nil {
			return fmt.Errorf("record activity for user %d: %w", a.UserID, err)
		}
	}
	return tx.Commit()
}

// UserActivity returns the last n hours for one user, most recent first, one
// row per node per hour.
func (s *Store) UserActivity(userID int64, hours int) ([]ActivityRow, error) {
	rows, err := s.db.Query(`
		SELECT hour, node_id, conns, peers, ports, ips, samples
		FROM user_activity
		WHERE user_id = ? AND hour > (unixepoch() / 3600) - ?
		ORDER BY hour DESC, node_id`, userID, hours)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ActivityRow
	for rows.Next() {
		var r ActivityRow
		if err := rows.Scan(&r.Hour, &r.NodeID, &r.Conns, &r.Peers,
			&r.Ports, &r.IPs, &r.Samples); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PruneActivity drops rows older than the retention window. Called on a timer:
// this table grows with every user, every node and every hour, and nothing else
// in the schema does.
func (s *Store) PruneActivity(keepHours int) error {
	_, err := s.db.Exec(
		`DELETE FROM user_activity WHERE hour < (unixepoch() / 3600) - ?`, keepHours)
	return err
}
