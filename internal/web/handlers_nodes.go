package web

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/kosje/skysbx-panel/internal/service"
	"github.com/kosje/skysbx-panel/internal/store"
)

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	s.renderNodes(w, r, http.StatusOK, "")
}

// renderNodesEditing renders one row as a form, through the same path as the
// plain list. The list polls itself every ten seconds, and a swap mid-edit
// replaces half-typed input — which is what made the edit form appear to close
// itself. Rendering the whole container is what lets the template turn the poll
// off while the form is open.
func (s *Server) renderNodesEditing(w http.ResponseWriter, r *http.Request, editID int64) {
	s.renderNodesFull(w, r, http.StatusOK, "", editID)
}

// renderNodes takes an optional freshly minted join token. It is the one thing
// on this page that cannot be re-read from the database — only its hash is
// stored — so it is passed through and shown once.
func (s *Server) renderNodes(w http.ResponseWriter, r *http.Request, code int, newToken string) {
	s.renderNodesFull(w, r, code, newToken, 0)
}

func (s *Server) renderNodesFull(w http.ResponseWriter, r *http.Request, code int,
	newToken string, editID int64,
) {
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
	// Same reason for the rejected set: whether the node adopted what it was
	// sent is state the database cannot hold, because the database holds what
	// the operator asked for.
	rejected := map[int64]bool{}
	connected := map[int64]bool{}
	// A bare IP in the address is legal and works, but it is what goes out in
	// every subscription: moving the node then means reissuing all of them, and
	// AnyTLS has no certificate that matches an address. Worth saying once, on
	// the row, rather than leaving it to be discovered.
	bareIP := map[int64]bool{}
	for _, n := range nodes {
		rejected[n.ID] = s.nodes.ApplyError(n.ID) != ""
		connected[n.ID] = s.nodes.Connected(n.ID)
		bareIP[n.ID] = net.ParseIP(strings.TrimSpace(n.Address)) != nil
	}

	data := map[string]any{"Nodes": nodes, "InboundCounts": counts,
		"Connected": connected, "Rejected": rejected, "NewToken": newToken,
		"EditID": editID, "BareIP": bareIP}

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

func (s *Server) editNode(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.errorBanner(w, http.StatusBadRequest, "bad node id")
		return
	}
	if _, err := s.svc.Node(id); err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderNodesEditing(w, r, id)
}

// updateNode applies an edit. The join token is not a field: it is shown once
// when it is issued, and replacing it is 换 token, which is a different and
// more disruptive act than fixing a typo in an address.
func (s *Server) updateNode(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.errorBanner(w, http.StatusBadRequest, "bad node id")
		return
	}
	n, err := s.svc.Node(id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	n.Name = strings.TrimSpace(r.FormValue("name"))
	n.Address = strings.TrimSpace(r.FormValue("address"))
	n.Country = strings.TrimSpace(r.FormValue("country"))

	if err := s.svc.UpdateNode(n); err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderNodes(w, r, http.StatusOK, "")
}

func (s *Server) toggleNode(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.errorBanner(w, http.StatusBadRequest, "bad node id")
		return
	}
	n, err := s.svc.Node(id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	n.Enabled = !n.Enabled
	if err := s.svc.UpdateNode(n); err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderNodes(w, r, http.StatusOK, "")
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
	s.renderInboundsFull(w, r, nodeID, code, 0)
}

// renderInboundsFull with editID set renders one row as a form, through the
// same path as the list. The list polls itself while a change is settling, and
// a swap mid-edit replaces half-typed input — rendering the whole container is
// what lets the template turn that poll off while the form is open.
func (s *Server) renderInboundsFull(w http.ResponseWriter, r *http.Request,
	nodeID int64, code int, editID int64,
) {
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
	// What the node says it is actually serving, so an inbound the node
	// rejected does not sit here looking enabled.
	tags, known := s.nodes.LiveInbounds(nodeID)
	live := map[int64]bool{}
	if known {
		for _, in := range inbounds {
			live[in.ID] = tags[in.Tag]
		}
	}

	applyErr := s.nodes.ApplyError(nodeID)

	settle := s.settle(r, nodeID, inbounds, tags, known, applyErr)
	if editID != 0 {
		settle = 0
	}

	data := map[string]any{"Node": node, "Inbounds": inbounds,
		"Protocols":        []string{store.ProtoVLESS, store.ProtoAnyTLS, store.ProtoShadowsocks},
		"DefaultHandshake": service.DefaultHandshake,
		"DefaultCertPath":  service.DefaultCertPath,
		"DefaultKeyPath":   service.DefaultKeyPath,
		"StateKnown":       known,
		"Live":             live,
		"NodeError":        applyErr,
		"Settle":           settle,
		"EditID":           editID}

	if editID != 0 {
		if err := s.inboundEditFields(data, inbounds, editID); err != nil {
			s.fail(w, r, err)
			return
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if r.Header.Get("HX-Request") == "true" {
		s.render(w, "inbound-table", data)
		return
	}
	data["Page"] = "inbounds"
	s.render(w, "inbounds", data)
}

// settleLimit bounds the poll below. Applying a configuration is two hops and a
// sing-box restart, normally under two seconds; past this the answer is that
// something is wrong, and the page should say what it knows rather than spin.
const settleLimit = 8

// settle returns the number of the next poll, or 0 when the page should stop
// polling.
//
// An inbound is written to the database and pushed to the node asynchronously,
// so the response to "create" is rendered before the node can possibly have
// applied it — every new inbound appeared as 未生效 until the operator reloaded
// by hand. The page asks again until the node's report catches up with what the
// panel holds.
//
// It stops as soon as there is a real answer: everything live, or the node
// having said why not. A disconnected node is also an answer, and one that no
// amount of polling improves.
func (s *Server) settle(r *http.Request, nodeID int64, inbounds []*store.Inbound,
	tags map[string]bool, known bool, applyErr string,
) int {
	if applyErr != "" || !s.nodes.Connected(nodeID) {
		return 0
	}
	pending := !known
	for _, in := range inbounds {
		if in.Enabled && !tags[in.Tag] {
			pending = true
			break
		}
	}
	if !pending {
		return 0
	}
	next, _ := strconv.Atoi(r.URL.Query().Get("settle"))
	if next+1 > settleLimit {
		return 0
	}
	return next + 1
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
		Address:    strings.TrimSpace(r.FormValue("address")),
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

// inboundEditFields fills in the current values of the protocol-specific fields
// for the row being edited, so the template can render them without knowing
// which protocol uses which.
func (s *Server) inboundEditFields(data map[string]any,
	inbounds []*store.Inbound, editID int64,
) error {
	var in *store.Inbound
	for _, candidate := range inbounds {
		if candidate.ID == editID {
			in = candidate
			break
		}
	}
	if in == nil {
		return store.ErrNotFound
	}

	client, err := service.ParseClient(in)
	if err != nil {
		return err
	}
	sb, err := service.ParseConfig(in)
	if err != nil {
		return err
	}

	handshake, tls := service.InboundEditFields(in.Protocol)
	data["EditHandshake"] = handshake
	data["EditTLS"] = tls
	data["SNI"] = client.SNI
	if sb.TLS != nil {
		data["CertPath"] = sb.TLS.CertificatePath
		data["KeyPath"] = sb.TLS.KeyPath
		if sb.TLS.Reality != nil {
			data["HandshakeValue"] = fmt.Sprintf("%s:%d",
				sb.TLS.Reality.Handshake.Server, sb.TLS.Reality.Handshake.ServerPort)
		}
	}
	return nil
}

func (s *Server) editInbound(w http.ResponseWriter, r *http.Request) {
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
	s.renderInboundsFull(w, r, in.NodeID, http.StatusOK, id)
}

func (s *Server) updateInbound(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.errorBanner(w, http.StatusBadRequest, "bad inbound id")
		return
	}
	port, err := strconv.Atoi(strings.TrimSpace(r.FormValue("port")))
	if err != nil {
		s.errorBanner(w, http.StatusBadRequest, "port must be a number")
		return
	}
	in, err := s.svc.EditInbound(id, service.InboundEdit{
		Port:       port,
		Address:    strings.TrimSpace(r.FormValue("address")),
		Handshake:  r.FormValue("handshake"),
		CertPath:   strings.TrimSpace(r.FormValue("cert_path")),
		KeyPath:    strings.TrimSpace(r.FormValue("key_path")),
		ServerName: strings.TrimSpace(r.FormValue("server_name")),
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderInbounds(w, r, in.NodeID, http.StatusOK)
}

func (s *Server) toggleInbound(w http.ResponseWriter, r *http.Request) {
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
	if err := s.svc.SetInboundEnabled(id, !in.Enabled); err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderInbounds(w, r, in.NodeID, http.StatusOK)
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
