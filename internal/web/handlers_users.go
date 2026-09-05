package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kosje/skysbx-panel/internal/service"
)

// nowFunc exists so tests can pin time without a clock abstraction threaded
// through every call.
var nowFunc = time.Now

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	s.renderUsers(w, r, http.StatusOK)
}

// renderUsers renders either the whole page or just the table, depending on
// whether htmx asked. Both paths go through here so a create and a plain page
// load can never disagree about what the list looks like.
func (s *Server) renderUsers(w http.ResponseWriter, r *http.Request, code int) {
	users, err := s.svc.Users()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data := map[string]any{"Users": users, "Now": nowFunc(),
		"Online": s.nodes.OnlineUsers()}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if r.Header.Get("HX-Request") == "true" {
		s.render(w, "user-table", data)
		return
	}
	data["Page"] = "users"
	s.render(w, "users", data)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	nu := service.NewUser{
		Name: r.FormValue("name"),
		Note: strings.TrimSpace(r.FormValue("note")),
	}

	if v := strings.TrimSpace(r.FormValue("expires_at")); v != "" {
		// The browser sends a date-only value; treat it as the end of that day
		// in local time, which is what someone typing "expires on the 5th"
		// means.
		t, err := time.ParseInLocation("2006-01-02", v, time.Local)
		if err != nil {
			s.errorBanner(w, http.StatusBadRequest, "expiry must be a date like 2026-01-31")
			return
		}
		t = t.Add(24*time.Hour - time.Second)
		nu.ExpiresAt = &t
	}

	if v := strings.TrimSpace(r.FormValue("traffic_limit_gb")); v != "" {
		gb, err := strconv.ParseFloat(v, 64)
		if err != nil || gb < 0 {
			s.errorBanner(w, http.StatusBadRequest, "traffic limit must be a number of GiB")
			return
		}
		nu.TrafficLimit = int64(gb * 1024 * 1024 * 1024)
	}

	if _, err := s.svc.CreateUser(nu); err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderUsers(w, r, http.StatusCreated)
}

func (s *Server) toggleUser(w http.ResponseWriter, r *http.Request) {
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
	u.Enabled = !u.Enabled
	if err := s.svc.UpdateUser(u); err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderUsers(w, r, http.StatusOK)
}

func (s *Server) resetUser(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.errorBanner(w, http.StatusBadRequest, "bad user id")
		return
	}
	if err := s.svc.ResetUserTraffic(id); err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderUsers(w, r, http.StatusOK)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.errorBanner(w, http.StatusBadRequest, "bad user id")
		return
	}
	if err := s.svc.DeleteUser(id); err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderUsers(w, r, http.StatusOK)
}
