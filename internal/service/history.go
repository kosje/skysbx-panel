package service

import (
	"time"

	"github.com/kosje/skysbx-panel/internal/store"
)

// DailyTotal is one day of traffic across every user and node.
type DailyTotal struct {
	Day   time.Time
	Bytes int64
}

// TrafficHistory returns the last n days, oldest first, with empty days filled
// in.
//
// The gaps matter: a chart drawn straight from the rows would silently close up
// a day with no traffic, turning an outage into a flat line between its
// neighbours rather than a visible hole.
func (s *Service) TrafficHistory(days int) ([]DailyTotal, error) {
	if days < 1 {
		days = 14
	}
	rows, err := s.st.TotalTrafficByDay(days)
	if err != nil {
		return nil, err
	}
	byDay := make(map[int64]int64, len(rows))
	for _, r := range rows {
		byDay[r.Day] = r.Up + r.Down
	}

	today := time.Now().UTC().Unix() / 86400
	out := make([]DailyTotal, 0, days)
	for d := today - int64(days) + 1; d <= today; d++ {
		out = append(out, DailyTotal{
			Day:   time.Unix(d*86400, 0).UTC(),
			Bytes: byDay[d],
		})
	}
	return out, nil
}

// UserTrafficHistory is the same for one user.
func (s *Service) UserTrafficHistory(userID int64, days int) ([]store.DailyUsage, error) {
	return s.st.UserTrafficHistory(userID, days)
}
