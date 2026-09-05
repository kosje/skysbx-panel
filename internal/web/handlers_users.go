package web

import (
	"fmt"
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
	// How many inbounds each user may use, so the list can say "全部" or "2/5"
	// without a query per row.
	inbounds, err := s.svc.Inbounds()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	restrictions, err := s.svc.Store().UserInboundMap()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	access := make(map[int64]int, len(users))
	for _, u := range users {
		if allowed, restricted := restrictions[u.ID]; restricted {
			access[u.ID] = len(allowed)
		} else {
			access[u.ID] = -1 // unrestricted
		}
	}

	data := map[string]any{"Users": users, "Now": nowFunc(),
		"Online": s.nodes.OnlineUsers(), "IPs": s.nodes.UserIPCounts(),
		"Access": access, "InboundCount": len(inbounds)}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if r.Header.Get("HX-Request") == "true" {
		s.render(w, "user-table", data)
		return
	}
	data["Page"] = "users"
	s.render(w, "users", data)
}

// expiryFromForm reads the date field. A blank value means no expiry, which is
// a nil pointer rather than the zero time — the zero time is in the past, and
// would lock everyone out.
func expiryFromForm(r *http.Request) (*time.Time, error) {
	v := strings.TrimSpace(r.FormValue("expires_at"))
	if v == "" {
		return nil, nil
	}
	// The browser sends a date-only value; treat it as the end of that day in
	// local time, which is what someone typing "expires on the 5th" means.
	t, err := time.ParseInLocation("2006-01-02", v, time.Local)
	if err != nil {
		return nil, fmt.Errorf("expiry must be a date like 2026-01-31")
	}
	t = t.Add(24*time.Hour - time.Second)
	return &t, nil
}

// ipLimitFromForm reads the concurrent-address cap. Blank and zero both mean
// no limit.
func ipLimitFromForm(r *http.Request) (int, error) {
	v := strings.TrimSpace(r.FormValue("ip_limit"))
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("address limit must be a whole number, or blank for no limit")
	}
	return n, nil
}

// limitFromForm reads the traffic field, in GiB. Blank and zero both mean no
// limit, which is how the rest of the panel reads a zero.
func limitFromForm(r *http.Request) (int64, error) {
	v := strings.TrimSpace(r.FormValue("traffic_limit_gb"))
	if v == "" {
		return 0, nil
	}
	gb, err := strconv.ParseFloat(v, 64)
	if err != nil || gb < 0 {
		return 0, fmt.Errorf("traffic limit must be a number of GiB")
	}
	return int64(gb * 1024 * 1024 * 1024), nil
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	nu := service.NewUser{
		Name: r.FormValue("name"),
		Note: strings.TrimSpace(r.FormValue("note")),
	}

	expires, err := expiryFromForm(r)
	if err != nil {
		s.errorBanner(w, http.StatusBadRequest, err.Error())
		return
	}
	nu.ExpiresAt = expires

	limit, err := limitFromForm(r)
	if err != nil {
		s.errorBanner(w, http.StatusBadRequest, err.Error())
		return
	}
	nu.TrafficLimit = limit

	ipLimit, err := ipLimitFromForm(r)
	if err != nil {
		s.errorBanner(w, http.StatusBadRequest, err.Error())
		return
	}
	nu.IPLimit = ipLimit

	if _, err := s.svc.CreateUser(nu); err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderUsers(w, r, http.StatusCreated)
}

// editUser swaps one row for a form over the same fields. The row is the target
// so the rest of the table — and anyone else's row mid-edit — is left alone.
func (s *Server) editUser(w http.ResponseWriter, r *http.Request) {
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.render(w, "user-edit-row", map[string]any{"User": u})
}

// updateUser applies the edit. Traffic used is not a field here and is not
// written by the store either: a stale figure in a form that was open while
// traffic was being reported must not roll someone's usage back.
func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
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

	expires, err := expiryFromForm(r)
	if err != nil {
		s.errorBanner(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, err := limitFromForm(r)
	if err != nil {
		s.errorBanner(w, http.StatusBadRequest, err.Error())
		return
	}

	ipLimit, err := ipLimitFromForm(r)
	if err != nil {
		s.errorBanner(w, http.StatusBadRequest, err.Error())
		return
	}

	u.Name = strings.TrimSpace(r.FormValue("name"))
	u.Note = strings.TrimSpace(r.FormValue("note"))
	u.ExpiresAt = expires
	u.TrafficLimit = limit
	u.IPLimit = ipLimit

	if err := s.svc.UpdateUser(u); err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderUsers(w, r, http.StatusOK)
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
