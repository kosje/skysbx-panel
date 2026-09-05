package store

// AddTraffic folds one reporting interval into both the daily rollup and each
// user's running total, in a single transaction.
//
// deltas maps user id to {up, down}. Both are added, never assigned: reports
// are deltas precisely so that a node restart cannot make a total go backwards.
func (s *Store) AddTraffic(nodeID, day int64, deltas map[int64][2]int64) error {
	if len(deltas) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for userID, d := range deltas {
		up, down := d[0], d[1]
		if _, err := tx.Exec(`
			INSERT INTO traffic (user_id, node_id, day, up, down)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(user_id, node_id, day) DO UPDATE SET
				up = up + excluded.up, down = down + excluded.down`,
			userID, nodeID, day, up, down); err != nil {
			return err
		}
		// users.traffic_used is the denormalised total the limit check reads on
		// every push. Keeping it here rather than summing the traffic table
		// keeps that check O(1) as history grows.
		if _, err := tx.Exec(
			`UPDATE users SET traffic_used = traffic_used + ? WHERE id = ?`,
			up+down, userID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DailyUsage is one row of a user's traffic history.
type DailyUsage struct {
	Day  int64
	Up   int64
	Down int64
}

// TotalTrafficByDay returns the last n days across every user and node, oldest
// first. Days with no traffic are simply absent — the caller fills them in,
// because a chart that closes up the gaps hides an outage.
func (s *Store) TotalTrafficByDay(days int) ([]DailyUsage, error) {
	rows, err := s.db.Query(`
		SELECT day, sum(up), sum(down) FROM traffic
		WHERE day > (unixepoch() / 86400) - ?
		GROUP BY day ORDER BY day`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DailyUsage
	for rows.Next() {
		var d DailyUsage
		if err := rows.Scan(&d.Day, &d.Up, &d.Down); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// UserTrafficHistory returns the last n days for a user, most recent first,
// summed across nodes.
func (s *Store) UserTrafficHistory(userID int64, days int) ([]DailyUsage, error) {
	rows, err := s.db.Query(`
		SELECT day, sum(up), sum(down) FROM traffic
		WHERE user_id = ? GROUP BY day ORDER BY day DESC LIMIT ?`, userID, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DailyUsage
	for rows.Next() {
		var d DailyUsage
		if err := rows.Scan(&d.Day, &d.Up, &d.Down); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
