// Package sub turns a user plus the inbounds they may use into whatever format
// their client asked for.
//
// The panel stores server-side configuration; a subscription is the client-side
// view of the same thing. The two are generated from one Entry list so that a
// share link, a sing-box config and a Clash config can never disagree about a
// port or a key.
package sub

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/kosje/skysbx-panel/internal/service"
	"github.com/kosje/skysbx-panel/internal/store"
)

// Entry is one connectable endpoint: a user's credentials against one inbound
// on one node.
type Entry struct {
	Name string // the inbound tag: unique, stable, and what proxies are named
	// Label is what a client's server list shows: the tag plus whose account
	// this is and what is left of it. Only the link-list format uses it; see
	// label() for why the other two keep the tag.
	Label    string
	Protocol string
	Address  string // the node's client-facing address
	Port     int

	// Credentials, already resolved for this protocol.
	UUID     string // VLESS
	Flow     string // VLESS
	Password string // AnyTLS, or the "serverPSK:userPSK" pair for Shadowsocks
	Method   string // Shadowsocks

	// TLS parameters.
	SNI string
	FP  string
	PBK string // Reality public key
	SID string // Reality short id
}

// Build assembles the entries for one user.
//
// Only enabled inbounds on enabled nodes appear, and only if the user is
// allowed on them. An inactive user gets an empty list rather than an error:
// their client should receive a valid, empty subscription and stop connecting,
// not an HTTP failure it will retry forever.
func Build(u *store.User, nodes []*store.Node, inbounds []*store.Inbound,
	allowed map[int64]bool,
) ([]Entry, error) {
	if !u.Active(nowFunc()) {
		return nil, nil
	}

	byNode := make(map[int64]*store.Node, len(nodes))
	for _, n := range nodes {
		if n.Enabled {
			byNode[n.ID] = n
		}
	}

	var out []Entry
	for _, in := range inbounds {
		if !in.Enabled {
			continue
		}
		node, ok := byNode[in.NodeID]
		if !ok {
			continue
		}
		// A nil map means unrestricted; a non-nil one lists the exceptions.
		if allowed != nil && !allowed[in.ID] {
			continue
		}

		client, err := service.ParseClient(in)
		if err != nil {
			return nil, err
		}

		// An inbound may be reached through a relay on a different host, in
		// which case that is what clients dial and the node's own address
		// never appears.
		address, port := node.Address, in.Port
		switch {
		case in.RelayNodeID != 0:
			// A relay run by a node this panel manages. byNode holds only
			// enabled nodes, so a disabled relay drops the entry rather than
			// falling back — falling back would hand out the node's own address
			// to everyone, which is the one thing the relay existed to prevent,
			// and it would happen without a word.
			relay, ok := byNode[in.RelayNodeID]
			if !ok {
				continue
			}
			address, port = relay.Address, in.RelayPort
		case in.Address != "":
			address, port = relayTarget(in.Address, in.Port)
		}

		e := Entry{
			Name:     in.Tag,
			Label:    label(in.Tag, u, nowFunc()),
			Protocol: in.Protocol,
			Address:  address,
			Port:     port,
			SNI:      client.SNI,
			FP:       client.FP,
			PBK:      client.PBK,
			SID:      client.SID,
		}

		switch in.Protocol {
		case store.ProtoVLESS:
			e.UUID = u.VlessUUID
			e.Flow = client.Flow
		case store.ProtoAnyTLS:
			e.Password = u.Password
		case store.ProtoShadowsocks:
			e.Method = client.Method
			// Shadowsocks 2022 authenticates with both halves of the key pair:
			// the inbound's shared PSK and the user's own, joined by a colon.
			// The server half alone would authenticate as nobody and bill to
			// nobody.
			e.Password = client.ServerPSK + ":" + u.SSPassword
		default:
			return nil, fmt.Errorf("inbound %s: unsupported protocol %q", in.Tag, in.Protocol)
		}
		out = append(out, e)
	}
	return out, nil
}

// relayTarget splits an inbound's relay address into what a client dials.
//
// The port is the point of the whole field. A relay exists to put the inbound
// on a port the node cannot have — usually 443, because that is the one port
// every network lets through — so "relay.example.net:443" has to mean 443 and
// not the node's own listen port. Without a port it is just a different host
// for the same port, which is the other thing relays are used for.
func relayTarget(address string, nodePort int) (string, int) {
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		// No port, or an IPv6 literal written without brackets. Either way
		// there is nothing to override.
		return strings.Trim(address, "[]"), nodePort
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return host, nodePort
	}
	return host, port
}
