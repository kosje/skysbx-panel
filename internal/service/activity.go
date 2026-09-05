package service

import (
	"time"

	"github.com/kosje/skysbx-panel/internal/store"
)

// ActivityRetentionHours is how long the per-hour digest is kept.
//
// Thirty days: long enough to answer "was this account being shared last week",
// short enough that a table which grows with users × nodes × hours stays a
// rounding error next to the traffic rows.
const ActivityRetentionHours = 30 * 24

// Activity is one node's account of what a user is doing right now.
type Activity struct {
	Conns int
	Peers int
	Ports int
	IPs   int
}

// RecordActivity stores a node's sample, keyed by hour.
//
// Names come in, ids go out: the node knows users by name because that is what
// sing-box authenticates, and a name that no longer maps to a user is dropped
// rather than resolved into whoever holds that name now.
func (s *Service) RecordActivity(nodeID int64, byName map[string]Activity) error {
	if len(byName) == 0 {
		return nil
	}
	users, err := s.st.Users()
	if err != nil {
		return err
	}
	ids := make(map[string]int64, len(users))
	for _, u := range users {
		ids[u.Name] = u.ID
	}

	samples := make([]store.ActivitySample, 0, len(byName))
	for name, a := range byName {
		id, ok := ids[name]
		if !ok {
			continue
		}
		samples = append(samples, store.ActivitySample{
			UserID: id, Conns: a.Conns, Peers: a.Peers, Ports: a.Ports, IPs: a.IPs,
		})
	}
	return s.st.RecordActivity(nodeID, time.Now().Unix()/3600, samples)
}

// UserActivity is the recent per-hour digest for one user.
func (s *Service) UserActivity(userID int64, hours int) ([]store.ActivityRow, error) {
	return s.st.UserActivity(userID, hours)
}

// PruneActivity drops digests past the retention window.
func (s *Service) PruneActivity() error {
	return s.st.PruneActivity(ActivityRetentionHours)
}

// ActivityRetentionDays is the window in the unit a page shows it in.
func (s *Service) ActivityRetentionDays() int { return ActivityRetentionHours / 24 }
