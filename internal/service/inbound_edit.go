package service

import (
	"encoding/json"
	"net"
	"strconv"
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

	// Address overrides what subscriptions point at for this inbound. Blank
	// means the node's own address.
	Address string

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
	if err := CheckRelayAddress(e.Address); err != nil {
		return nil, err
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
	// Compared before the fields are overwritten. The address is the one thing
	// here the node never sees — it changes where clients are told to connect,
	// not where anything listens — so an address-only edit must not cost every
	// live connection on the node a listener rebuild.
	nodeVisibleChange := in.Port != e.Port || in.Config != string(cfg)

	in.Port = e.Port
	in.Config = string(cfg)
	in.Client = string(cl)
	in.Address = strings.TrimSpace(e.Address)

	if err := s.st.UpdateInbound(in); err != nil {
		return nil, err
	}
	if nodeVisibleChange {
		s.notify.ConfigChanged(in.NodeID)
	}
	return in, nil
}

// CheckRelayAddress rejects an address the subscription generator would end up
// quietly reinterpreting.
//
// It parses the same way the generator does, and the generator falls back to
// the node's port when it cannot make sense of what follows the colon. That is
// the right behaviour there — a subscription must still be servable — and the
// wrong behaviour here, where a typo should come back as an error while the
// operator is still looking at the field.
func CheckRelayAddress(address string) error {
	address = strings.TrimSpace(address)
	if address == "" {
		return nil
	}
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		// No colon at all, or a bare IPv6 literal. Both are a host on the
		// inbound's own port, which is fine.
		if strings.Count(address, ":") <= 1 && strings.Contains(address, ":") {
			return invalid("relay address %q: expected host or host:port", address)
		}
		return nil
	}
	if host == "" {
		return invalid("relay address %q has no host", address)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return invalid("relay address %q: %q is not a port", address, portStr)
	}
	return nil
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
