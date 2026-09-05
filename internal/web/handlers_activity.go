package web

import (
	"net/http"
	"time"

	"github.com/kosje/skysbx-panel/internal/store"
)

// activityHours is the window the page shows. A week is enough to see a pattern
// and short enough to read; the rows are kept for thirty days either way.
const activityHours = 7 * 24

// suspiciousPeers is the peer count above which a row is flagged.
//
// A browser talking to one page reaches a handful of hosts. A torrent client
// talks to as many peers as it can find, which is what makes the count, rather
// than the protocol, the reliable signal — it does not care whether the payload
// was encrypted.
//
// It is a hint, not a verdict: a busy tab-hoarder can cross it, and the page
// says so rather than labelling anyone.
const suspiciousPeers = 40

type activityRow struct {
	When  time.Time
	Node  string
	Conns int
	Peers int
	Ports int
	IPs   int
	Busy  bool
}

func (s *Server) getUserActivity(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.errorBanner(w, http.StatusBadRequest, "bad user id")
		return
	}
	u, err := s.svc.User(id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	nodes, err := s.svc.Nodes()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	names := make(map[int64]string, len(nodes))
	for _, n := range nodes {
		names[n.ID] = n.Name
	}

	raw, err := s.svc.UserActivity(id, activityHours)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rows := make([]activityRow, 0, len(raw))
	peak := store.ActivityRow{}
	for _, a := range raw {
		name := names[a.NodeID]
		if name == "" {
			name = "(已删除)"
		}
		rows = append(rows, activityRow{
			When: time.Unix(a.Hour*3600, 0).Local(), Node: name,
			Conns: a.Conns, Peers: a.Peers, Ports: a.Ports, IPs: a.IPs,
			Busy: a.Peers >= suspiciousPeers,
		})
		if a.Peers > peak.Peers {
			peak = a
		}
	}

	s.page(w, "activity", map[string]any{
		"User": u, "Rows": rows, "Hours": activityHours,
		"Peak": peak, "Threshold": suspiciousPeers,
		"LiveIPs":   s.nodes.UserIPCounts()[u.Name],
		"Retention": s.svc.ActivityRetentionDays(),
		"Page":      "users",
	})
}
