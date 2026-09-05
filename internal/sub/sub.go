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

	"github.com/kosje/skysb-panel/internal/service"
	"github.com/kosje/skysb-panel/internal/store"
)

// Entry is one connectable endpoint: a user's credentials against one inbound
// on one node.
type Entry struct {
	Name     string // what the client displays; the inbound tag, which is unique
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

		e := Entry{
			Name:     in.Tag,
			Protocol: in.Protocol,
			Address:  node.Address,
			Port:     in.Port,
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
