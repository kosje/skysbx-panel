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
//
// A node may only bill users it was actually given. Without that check any node
// can charge traffic to any account on the panel, and since crossing the limit
// revokes access, one compromised node could disable every other node's users
// by reporting a large enough number for each of them. Nodes run on rented
// machines; that one of them is eventually not yours is the assumption worth
// designing to.
func (s *Service) RecordTraffic(nodeID int64, usage map[string]Usage) error {
	if len(usage) == 0 {
		return nil
	}
	byName, err := s.nodeUserIDs(nodeID)
	if err != nil {
		return err
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
		// A single interval is thirty seconds. Even a saturated 100 Gbit/s link
		// moves under 400 GiB in that time, so anything past this is not a
		// measurement — and unlike a wrong small number, a wrong enormous one
		// permanently revokes an account that nothing will restore.
		if u.Up > maxReportedDelta || u.Down > maxReportedDelta {
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

// maxReportedDelta bounds one interval's figure for one user. 1 TiB is far
// above anything a real link produces in thirty seconds and far below the range
// where an int64 total could be pushed past a quota in a single frame.
const maxReportedDelta = 1 << 40

// nodeUserIDs is the set of users this node is entitled to say anything about,
// by the name it knows them by.
//
// Membership, not activity: a user who has just expired or run out of quota is
// no longer pushed to the node, but the node is still holding their last
// half-minute of traffic and that report should land. What it excludes is the
// account that was never on this node at all.
func (s *Service) nodeUserIDs(nodeID int64) (map[string]int64, error) {
	inbounds, err := s.st.NodeInbounds(nodeID)
	if err != nil {
		return nil, err
	}
	onNode := make(map[int64]bool, len(inbounds))
	for _, in := range inbounds {
		onNode[in.ID] = true
	}

	users, err := s.st.Users()
	if err != nil {
		return nil, err
	}
	restrictions, err := s.st.UserInboundMap()
	if err != nil {
		return nil, err
	}

	out := make(map[string]int64, len(users))
	for _, u := range users {
		allowed, restricted := restrictions[u.ID]
		if !restricted {
			// No rows means every inbound, so every node.
			out[u.Name] = u.ID
			continue
		}
		for id := range allowed {
			if onNode[id] {
				out[u.Name] = u.ID
				break
			}
		}
	}
	return out, nil
}

func toStoreDeltas(in map[int64]Usage) map[int64][2]int64 {
	out := make(map[int64][2]int64, len(in))
	for id, u := range in {
		out[id] = [2]int64{u.Up, u.Down}
	}
	return out
}
