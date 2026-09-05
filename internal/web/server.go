// Package web serves the admin UI: plain Go templates plus htmx, no build step
// and no bundler.
//
// Handlers here are deliberately thin — read the form, call the service, render
// a template. All the logic lives in internal/service, so this package can be
// replaced by a JSON API and a single-page frontend without touching anything
// that matters.
package web

import (
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/kosje/skysbx-panel/internal/service"
	"github.com/kosje/skysbx-panel/internal/store"
)

//go:embed templates/*.html static/*
var assets embed.FS

const settingSessionKey = "web.session_key"

type Server struct {
	svc   *service.Service
	log   *slog.Logger
	tpl   *template.Template
	sess  *sessions
	nodes NodeChannel

	// secureCookies marks session cookies Secure. Off when serving plain HTTP
	// on localhost, because a Secure cookie is simply dropped there and login
	// would appear to succeed and then not stick.
	secureCookies bool
}

// NodeChannel is the node control channel, mounted by the router. It is an
// interface so that the web package does not depend on the hub, and — more to
// the point — so that forgetting to pass one is a compile error rather than a
// route that quietly 404s every node that dials in.
type NodeChannel interface {
	Handler() http.HandlerFunc
	Connected(nodeID int64) bool
	OnlineUsers() map[string]bool
}

func New(svc *service.Service, nodes NodeChannel, log *slog.Logger, secureCookies bool) (*Server, error) {
	tpl, err := template.New("").Funcs(templateFuncs()).ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}

	// The session key lives in the database so restarts do not log everyone out.
	stored, err := svc.Store().Setting(settingSessionKey)
	if err != nil {
		return nil, err
	}
	var key []byte
	if stored == "" {
		key = newSessionKey()
		if err := svc.Store().SetSetting(settingSessionKey,
			base64.StdEncoding.EncodeToString(key)); err != nil {
			return nil, err
		}
	} else {
		key, err = base64.StdEncoding.DecodeString(stored)
		if err != nil {
			return nil, fmt.Errorf("stored session key is corrupt: %w", err)
		}
	}

	if nodes == nil {
		return nil, fmt.Errorf("a node channel is required")
	}
	return &Server{svc: svc, nodes: nodes, log: log, tpl: tpl,
		sess: newSessions(key), secureCookies: secureCookies}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	static, err := fs.Sub(assets, "static")
	if err != nil {
		// Impossible: the directory is embedded at compile time.
		panic("skysbx: embedded static assets missing: " + err.Error())
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))

	// The token in the path is the credential; this is the one unauthenticated
	// route that returns anything.
	mux.HandleFunc("GET /sub/{token}", s.getSubscription)

	// The node control channel. Nodes authenticate with their own bearer token,
	// so this sits outside the session gate.
	mux.HandleFunc("GET /api/v1/node/connect", s.nodes.Handler())

	// Open routes.
	mux.HandleFunc("GET /setup", s.getSetup)
	mux.HandleFunc("POST /setup", s.postSetup)
	mux.HandleFunc("GET /login", s.getLogin)
	mux.HandleFunc("POST /login", s.postLogin)
	mux.HandleFunc("POST /logout", s.postLogout)

	// Everything else needs a session.
	mux.Handle("GET /{$}", s.auth(http.HandlerFunc(s.getDashboard)))

	mux.Handle("GET /users", s.auth(http.HandlerFunc(s.listUsers)))
	mux.Handle("POST /users", s.auth(http.HandlerFunc(s.createUser)))
	mux.Handle("POST /users/{id}/toggle", s.auth(http.HandlerFunc(s.toggleUser)))
	mux.Handle("POST /users/{id}/reset", s.auth(http.HandlerFunc(s.resetUser)))
	mux.Handle("DELETE /users/{id}", s.auth(http.HandlerFunc(s.deleteUser)))

	mux.Handle("GET /nodes", s.auth(http.HandlerFunc(s.listNodes)))
	mux.Handle("POST /nodes", s.auth(http.HandlerFunc(s.createNode)))
	mux.Handle("POST /nodes/{id}/rotate", s.auth(http.HandlerFunc(s.rotateNode)))
	mux.Handle("DELETE /nodes/{id}", s.auth(http.HandlerFunc(s.deleteNode)))

	mux.Handle("GET /nodes/{id}/inbounds", s.auth(http.HandlerFunc(s.listInbounds)))
	mux.Handle("POST /nodes/{id}/inbounds", s.auth(http.HandlerFunc(s.createInbound)))
	mux.Handle("DELETE /inbounds/{id}", s.auth(http.HandlerFunc(s.deleteInbound)))

	return mux
}

// auth gates a handler on a valid session. It also handles the first-run case:
// with no administrator configured yet, everything redirects to /setup.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exists, err := s.svc.AdminExists()
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if !exists {
			s.redirect(w, r, "/setup")
			return
		}
		if _, err := s.sess.user(r); err != nil {
			s.redirect(w, r, "/login")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// redirect works for both a normal navigation and an htmx request. htmx
// swallows a 302 by following it with XHR and swapping the result into a
// fragment, which would nest a whole login page inside a table; HX-Redirect
// tells it to navigate instead.
func (s *Server) redirect(w http.ResponseWriter, r *http.Request, to string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", to)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// fail logs the real error and shows the user a short one. Validation problems
// are the user's to fix, so those are shown verbatim.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrInvalid):
		s.errorBanner(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "invalid: "))
	case errors.Is(err, store.ErrConflict):
		// A duplicate name is something the operator fixes by typing another
		// one, not an internal failure. Saying so beats "something went wrong".
		s.errorBanner(w, http.StatusConflict, "that name or tag is already taken")
	case errors.Is(err, store.ErrNotFound):
		s.errorBanner(w, http.StatusNotFound, "not found")
	default:
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		s.errorBanner(w, http.StatusInternalServerError, "something went wrong")
	}
}

func (s *Server) errorBanner(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	s.render(w, "error-banner", msg)
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	if err := s.tpl.ExecuteTemplate(w, name, data); err != nil {
		// The response is already partly written, so there is nothing useful to
		// send. Log it so a broken template does not vanish silently.
		s.log.Error("render template", "template", name, "error", err)
	}
}

func (s *Server) page(w http.ResponseWriter, name string, data map[string]any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if data == nil {
		data = map[string]any{}
	}
	data["Page"] = name
	s.render(w, name, data)
}

func pathID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}
