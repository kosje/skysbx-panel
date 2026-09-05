package web

import (
	"net/http"
	"strconv"

	"github.com/kosje/skysbx-panel/internal/store"
)

// accessGroup is one node's inbounds with the user's choice already applied, so
// the template does no lookups.
type accessGroup struct {
	Node   *store.Node
	Rows   []accessRow
	Chosen int
}

type accessRow struct {
	Inbound *store.Inbound
	Allowed bool
}

func (s *Server) getUserAccess(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.errorBanner(w, http.StatusBadRequest, "bad user id")
		return
	}
	s.renderUserAccess(w, r, id, http.StatusOK)
}

func (s *Server) renderUserAccess(w http.ResponseWriter, r *http.Request,
	userID int64, code int,
) {
	u, err := s.svc.User(userID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	nodes, err := s.svc.Nodes()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	inbounds, err := s.svc.Inbounds()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ids, err := s.svc.UserInboundIDs(userID)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// No rows at all means unrestricted, and the checkboxes then show every
	// inbound ticked: that is what "this user can use all of them" looks like,
	// and ticking them all back is the same state.
	unrestricted := len(ids) == 0
	allowed := make(map[int64]bool, len(ids))
	for _, id := range ids {
		allowed[id] = true
	}

	groups := make([]accessGroup, 0, len(nodes))
	for _, n := range nodes {
		g := accessGroup{Node: n}
		for _, in := range inbounds {
			if in.NodeID != n.ID {
				continue
			}
			ok := unrestricted || allowed[in.ID]
			if ok {
				g.Chosen++
			}
			g.Rows = append(g.Rows, accessRow{Inbound: in, Allowed: ok})
		}
		if len(g.Rows) > 0 {
			groups = append(groups, g)
		}
	}

	data := map[string]any{
		"User": u, "Groups": groups, "Unrestricted": unrestricted,
		"Page": "users",
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if r.Header.Get("HX-Request") == "true" {
		s.render(w, "access-form", data)
		return
	}
	s.render(w, "access", data)
}

func (s *Server) setUserAccess(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.errorBanner(w, http.StatusBadRequest, "bad user id")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.errorBanner(w, http.StatusBadRequest, "bad form")
		return
	}

	inbounds, err := s.svc.Inbounds()
	if err != nil {
		s.fail(w, r, err)
		return
	}

	picked := make([]int64, 0, len(r.Form["inbound"]))
	for _, v := range r.Form["inbound"] {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			s.errorBanner(w, http.StatusBadRequest, "bad inbound id")
			return
		}
		picked = append(picked, n)
	}

	// Everything ticked is stored as no restriction rather than one row per
	// inbound. Otherwise a later inbound would be invisible to every user who
	// had "all" selected before it existed — silently, and only noticed when
	// someone's subscription came back short.
	if len(picked) == len(inbounds) {
		picked = nil
	}

	if err := s.svc.SetUserInbounds(id, picked); err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderUserAccess(w, r, id, http.StatusOK)
}
