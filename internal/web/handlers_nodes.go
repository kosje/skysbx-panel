package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/kosje/skysbx-panel/internal/service"
	"github.com/kosje/skysbx-panel/internal/store"
)

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	s.renderNodes(w, r, http.StatusOK, "")
}

// renderNodes takes an optional freshly minted join token. It is the one thing
// on this page that cannot be re-read from the database — only its hash is
// stored — so it is passed through and shown once.
func (s *Server) renderNodes(w http.ResponseWriter, r *http.Request, code int, newToken string) {
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
	counts := map[int64]int{}
	for _, in := range inbounds {
		counts[in.NodeID]++
	}

	// Connection state lives in the hub, not the database: last_seen_at records
	// the last handshake, which says nothing about whether the channel is up now.
	connected := map[int64]bool{}
	for _, n := range nodes {
		connected[n.ID] = s.nodes.Connected(n.ID)
	}

	data := map[string]any{"Nodes": nodes, "InboundCounts": counts,
		"Connected": connected, "NewToken": newToken}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if r.Header.Get("HX-Request") == "true" {
		s.render(w, "node-table", data)
		return
	}
	data["Page"] = "nodes"
	s.render(w, "nodes", data)
}

func (s *Server) createNode(w http.ResponseWriter, r *http.Request) {
	_, token, err := s.svc.CreateNode(
		r.FormValue("name"), r.FormValue("address"), r.FormValue("country"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderNodes(w, r, http.StatusCreated, token)
}

func (s *Server) rotateNode(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.errorBanner(w, http.StatusBadRequest, "bad node id")
		return
	}
	token, err := s.svc.RotateNodeToken(id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderNodes(w, r, http.StatusOK, token)
}

func (s *Server) deleteNode(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.errorBanner(w, http.StatusBadRequest, "bad node id")
		return
	}
	if err := s.svc.DeleteNode(id); err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderNodes(w, r, http.StatusOK, "")
}

// ── inbounds ────────────────────────────────────────────────────────────────

func (s *Server) listInbounds(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.errorBanner(w, http.StatusBadRequest, "bad node id")
		return
	}
	s.renderInbounds(w, r, id, http.StatusOK)
}

func (s *Server) renderInbounds(w http.ResponseWriter, r *http.Request, nodeID int64, code int) {
	node, err := s.svc.Node(nodeID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	inbounds, err := s.svc.NodeInbounds(nodeID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data := map[string]any{"Node": node, "Inbounds": inbounds,
		"Protocols":        []string{store.ProtoVLESS, store.ProtoAnyTLS, store.ProtoShadowsocks},
		"DefaultHandshake": service.DefaultHandshake}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if r.Header.Get("HX-Request") == "true" {
		s.render(w, "inbound-table", data)
		return
	}
	data["Page"] = "inbounds"
	s.render(w, "inbounds", data)
}

func (s *Server) createInbound(w http.ResponseWriter, r *http.Request) {
	nodeID, err := pathID(r)
	if err != nil {
		s.errorBanner(w, http.StatusBadRequest, "bad node id")
		return
	}
	port, err := strconv.Atoi(strings.TrimSpace(r.FormValue("port")))
	if err != nil {
		s.errorBanner(w, http.StatusBadRequest, "port must be a number")
		return
	}

	spec := service.InboundSpec{
		Protocol:   r.FormValue("protocol"),
		Tag:        r.FormValue("tag"),
		Port:       port,
		Handshake:  r.FormValue("handshake"),
		CertPath:   strings.TrimSpace(r.FormValue("cert_path")),
		KeyPath:    strings.TrimSpace(r.FormValue("key_path")),
		ServerName: strings.TrimSpace(r.FormValue("server_name")),
	}
	// AnyTLS presents the node's own domain, which is exactly the address
	// clients already connect to. Defaulting saves retyping it and saves the
	// mismatch that follows a typo.
	if spec.Protocol == store.ProtoAnyTLS && spec.ServerName == "" {
		if n, err := s.svc.Node(nodeID); err == nil {
			spec.ServerName = n.Address
		}
	}

	if _, err := s.svc.CreateInbound(nodeID, spec); err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderInbounds(w, r, nodeID, http.StatusCreated)
}

func (s *Server) deleteInbound(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.errorBanner(w, http.StatusBadRequest, "bad inbound id")
		return
	}
	in, err := s.svc.Store().Inbound(id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.svc.DeleteInbound(id); err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderInbounds(w, r, in.NodeID, http.StatusOK)
}
