package web

import (
	"errors"
	"net/http"

	"github.com/kosje/skysb-panel/internal/service"
)

func (s *Server) getSetup(w http.ResponseWriter, r *http.Request) {
	exists, err := s.svc.AdminExists()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if exists {
		// Setup is one-shot. Leaving it open would let anyone who reaches the
		// panel replace the administrator's password.
		s.redirect(w, r, "/login")
		return
	}
	s.page(w, "setup", nil)
}

func (s *Server) postSetup(w http.ResponseWriter, r *http.Request) {
	exists, err := s.svc.AdminExists()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if exists {
		s.redirect(w, r, "/login")
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")
	if password != r.FormValue("password2") {
		s.errorBanner(w, http.StatusBadRequest, "the two passwords do not match")
		return
	}
	if err := s.svc.SetAdmin(username, password); err != nil {
		s.fail(w, r, err)
		return
	}
	s.sess.issue(w, username, s.secureCookies)
	s.redirect(w, r, "/")
}

func (s *Server) getLogin(w http.ResponseWriter, r *http.Request) {
	exists, err := s.svc.AdminExists()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !exists {
		s.redirect(w, r, "/setup")
		return
	}
	if _, err := s.sess.user(r); err == nil {
		s.redirect(w, r, "/")
		return
	}
	s.page(w, "login", nil)
}

func (s *Server) postLogin(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	if err := s.svc.CheckAdmin(username, r.FormValue("password")); err != nil {
		if errors.Is(err, service.ErrBadCredentials) {
			// One message for both wrong username and wrong password: telling
			// them apart is free reconnaissance.
			s.log.Warn("failed login", "username", username, "remote", r.RemoteAddr)
			s.errorBanner(w, http.StatusUnauthorized, "wrong username or password")
			return
		}
		s.fail(w, r, err)
		return
	}
	s.sess.issue(w, username, s.secureCookies)
	s.redirect(w, r, "/")
}

func (s *Server) postLogout(w http.ResponseWriter, r *http.Request) {
	s.sess.clear(w, s.secureCookies)
	s.redirect(w, r, "/login")
}

func (s *Server) getDashboard(w http.ResponseWriter, r *http.Request) {
	users, err := s.svc.Users()
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

	var totalTraffic int64
	active := 0
	for _, u := range users {
		totalTraffic += u.TrafficUsed
		if u.Active(nowFunc()) {
			active++
		}
	}
	history, err := s.svc.TrafficHistory(14)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	online := s.nodes.OnlineUsers()
	nodesUp := 0
	for _, n := range nodes {
		if s.nodes.Connected(n.ID) {
			nodesUp++
		}
	}

	s.page(w, "dashboard", map[string]any{
		"Users": len(users), "ActiveUsers": active, "OnlineUsers": len(online),
		"Nodes": len(nodes), "NodesUp": nodesUp, "Inbounds": len(inbounds),
		"Traffic": totalTraffic,
		"Chart":   trafficChart(history),
	})
}
