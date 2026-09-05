package service

import "time"

// Usage is one reporting interval's traffic for one user, as a delta.
type Usage struct {
	Up   int64
	Down int64
}

// MarkNodeSeen records that a node completed its handshake.
func (s *Service) MarkNodeSeen(nodeID int64, version string) error {
	return s.st.MarkNodeSeen(nodeID, version)
}

// RecordTraffic folds one node's report into the running totals.
//
// Reports are deltas, so this adds rather than assigns. A node that restarts
// loses its own counters and simply reports smaller numbers afterwards; with
// cumulative values the same restart would look like every user's usage jumping
// backwards, and there would be no way to tell that from a real reset.
//
// Users the panel does not know are ignored: a node can still be running a user
// list from before a deletion, and inventing a row for a name with no user
// would break the foreign key anyway.
func (s *Service) RecordTraffic(nodeID int64, usage map[string]Usage) error {
	if len(usage) == 0 {
		return nil
	}
	users, err := s.st.Users()
	if err != nil {
		return err
	}
	byName := make(map[string]int64, len(users))
	for _, u := range users {
		byName[u.Name] = u.ID
	}

	deltas := make(map[int64]Usage, len(usage))
	for name, u := range usage {
		if u.Up == 0 && u.Down == 0 {
			continue
		}
		id, ok := byName[name]
		if !ok {
			continue
		}
		deltas[id] = Usage{Up: u.Up, Down: u.Down}
	}
	if len(deltas) == 0 {
		return nil
	}

	day := time.Now().UTC().Unix() / 86400
	if err := s.st.AddTraffic(nodeID, day, toStoreDeltas(deltas)); err != nil {
		return err
	}

	// Crossing a traffic limit revokes access, and access is revoked by leaving
	// the user out of the next push — so the nodes have to be told.
	s.notify.UsersChanged()
	return nil
}

func toStoreDeltas(in map[int64]Usage) map[int64][2]int64 {
	out := make(map[int64][2]int64, len(in))
	for id, u := range in {
		out[id] = [2]int64{u.Up, u.Down}
	}
	return out
}
