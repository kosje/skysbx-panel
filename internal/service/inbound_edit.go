package service

import (
	"encoding/json"
	"strings"

	"github.com/kosje/skysbx-panel/internal/store"
)

// InboundEdit is the subset of an inbound that can be changed after it exists.
//
// The protocol and the tag are not in it. The tag addresses this inbound in the
// configuration pushed to the node and in every user list keyed by it, and the
// protocol decides which credential each user authenticates with — changing
// either is not an edit, it is a different inbound.
type InboundEdit struct {
	Port int

	// VLESS: the site whose handshake is borrowed. Reality's key pair is
	// deliberately absent; see EditInbound.
	Handshake string

	// AnyTLS.
	CertPath   string
	KeyPath    string
	ServerName string
}

// EditInbound changes an existing inbound in place.
//
// The generated secrets are carried over untouched: Reality's key pair and
// short id, and the Shadowsocks server PSK. Rebuilding the inbound from a spec
// would mint new ones, and every client holding the old subscription would stop
// connecting — with nothing in any log to say why, because a wrong Reality key
// looks exactly like a probe.
func (s *Service) EditInbound(id int64, e InboundEdit) (*store.Inbound, error) {
	in, err := s.st.Inbound(id)
	if err != nil {
		return nil, err
	}
	if e.Port < 1 || e.Port > 65535 {
		return nil, invalid("port %d out of range", e.Port)
	}

	sb, err := ParseConfig(in)
	if err != nil {
		return nil, err
	}
	client, err := ParseClient(in)
	if err != nil {
		return nil, err
	}

	sb.ListenPort = e.Port

	switch in.Protocol {
	case store.ProtoVLESS:
		host, port, err := splitHandshake(e.Handshake)
		if err != nil {
			return nil, err
		}
		// The handshake host is also the SNI a client presents, so the two move
		// together or Reality rejects every connection.
		sb.TLS.ServerName = host
		sb.TLS.Reality.Handshake.Server = host
		sb.TLS.Reality.Handshake.ServerPort = port
		client.SNI = host

	case store.ProtoAnyTLS:
		if e.ServerName == "" {
			return nil, invalid("anytls needs a server name")
		}
		if e.CertPath == "" {
			e.CertPath = DefaultCertPath
		}
		if e.KeyPath == "" {
			e.KeyPath = DefaultKeyPath
		}
		sb.TLS.ServerName = e.ServerName
		sb.TLS.CertificatePath = e.CertPath
		sb.TLS.KeyPath = e.KeyPath
		client.SNI = e.ServerName

	case store.ProtoShadowsocks:
		// Nothing but the port. The method is fixed and the server PSK is the
		// thing that must not change.

	default:
		return nil, invalid("unsupported protocol %q", in.Protocol)
	}

	cfg, err := json.Marshal(sb)
	if err != nil {
		return nil, err
	}
	cl, err := json.Marshal(client)
	if err != nil {
		return nil, err
	}
	in.Port = e.Port
	in.Config = string(cfg)
	in.Client = string(cl)

	if err := s.st.UpdateInbound(in); err != nil {
		return nil, err
	}
	// A port or certificate change is a listener rebuild, so the node has to be
	// told; connections on this node's other inbounds go with it, which is why
	// this is a config push and not a user push.
	s.notify.ConfigChanged(in.NodeID)
	return in, nil
}

// InboundEditFields is what the edit form should show for a protocol, so the
// UI does not have to know which fields each one uses.
func InboundEditFields(protocol string) (handshake, tls bool) {
	switch strings.TrimSpace(protocol) {
	case store.ProtoVLESS:
		return true, false
	case store.ProtoAnyTLS:
		return false, true
	default:
		return false, false
	}
}
