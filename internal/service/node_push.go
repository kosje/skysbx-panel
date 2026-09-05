package service

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/kosje/skysbx-panel/internal/singbox"
	"github.com/kosje/skysbx-panel/internal/store"
)

// Addresses the node binds its own API services to. Loopback only: these carry
// no authentication and exist purely so the node can read its own counters.
const (
	clashAPIAddr = "127.0.0.1:9090"
	v2rayAPIAddr = "127.0.0.1:6450"
)

// NodeConfig builds the sing-box configuration for one node.
//
// Inbound user lists are left empty on purpose. Users travel in their own
// message so that adding or removing one is a hot swap rather than a listener
// rebuild — a rebuild drops every live connection on that inbound.
func (s *Service) NodeConfig(nodeID int64) (*singbox.Config, error) {
	node, err := s.st.Node(nodeID)
	if err != nil {
		return nil, err
	}
	inbounds, err := s.st.NodeInbounds(nodeID)
	if err != nil {
		return nil, err
	}
	// A disabled node serves nothing. Leaving its listeners up and merely
	// hiding it from subscriptions would mean anyone holding an old link keeps
	// getting through — which is not what turning a node off means.
	if !node.Enabled {
		inbounds = nil
	}

	// What this node refuses to carry. Part of the configuration rather than
	// the user list because it is a property of the node's routing, not of who
	// is connected.
	policy, err := s.Policy()
	if err != nil {
		return nil, err
	}
	var route *singbox.ServerRoute
	if rules := policy.routeRules(); len(rules) > 0 {
		route = &singbox.ServerRoute{Rules: rules}
	}

	cfg := &singbox.Config{
		Log:       &singbox.Log{Level: "warn", Timestamp: true},
		Inbounds:  make([]singbox.Inbound, 0, len(inbounds)),
		Outbounds: []singbox.Outbound{{Type: "direct", Tag: "direct"}},
		Route:     route,
		Experimental: &singbox.Experimental{
			ClashAPI: &singbox.ClashAPI{ExternalController: clashAPIAddr},
			V2RayAPI: &singbox.V2RayAPI{
				Listen: v2rayAPIAddr,
				// Stats.Users is deliberately empty. It is an allowlist built
				// when the config loads, so anything written here is stale the
				// moment the first user changes — and a user missing from it
				// relays traffic that is never counted, silently. The node
				// rebuilds it from each users message instead.
				Stats: singbox.Stats{Enabled: true},
			},
		},
	}

	for _, in := range inbounds {
		if !in.Enabled {
			continue
		}
		var sb singbox.Inbound
		if err := json.Unmarshal([]byte(in.Config), &sb); err != nil {
			return nil, fmt.Errorf("inbound %s: stored config is not valid JSON: %w", in.Tag, err)
		}
		sb.Users = nil
		cfg.Inbounds = append(cfg.Inbounds, sb)
	}
	return cfg, nil
}

// NodeUsers builds the authoritative user list for one node, keyed by inbound
// tag. Only users that are currently active appear: revoking access is done by
// omission, so there is no separate disable command for a node to get wrong.
func (s *Service) NodeUsers(nodeID int64) (map[string][]singbox.User, error) {
	inbounds, err := s.st.NodeInbounds(nodeID)
	if err != nil {
		return nil, err
	}
	users, err := s.st.Users()
	if err != nil {
		return nil, err
	}
	restrictions, err := s.st.UserInboundMap()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	out := make(map[string][]singbox.User, len(inbounds))

	for _, in := range inbounds {
		if !in.Enabled {
			continue
		}
		list := make([]singbox.User, 0, len(users))
		for _, u := range users {
			if !u.Active(now) {
				continue
			}
			// No restriction rows means every inbound, which is the common
			// case for a single-tenant panel.
			if allowed, restricted := restrictions[u.ID]; restricted && !allowed[in.ID] {
				continue
			}
			su, ok := singboxUser(u, in.Protocol)
			if !ok {
				continue
			}
			list = append(list, su)
		}
		out[in.Tag] = list
	}
	return out, nil
}

// singboxUser picks the credential the protocol actually authenticates with.
// Sending all three would leak two secrets per user into every inbound's
// config for no benefit.
func singboxUser(u *store.User, protocol string) (singbox.User, bool) {
	switch protocol {
	case store.ProtoVLESS:
		return singbox.User{Name: u.Name, UUID: u.VlessUUID, Flow: FlowVision}, true
	case store.ProtoAnyTLS:
		return singbox.User{Name: u.Name, Password: u.Password}, true
	case store.ProtoShadowsocks:
		return singbox.User{Name: u.Name, Password: u.SSPassword}, true
	default:
		return singbox.User{}, false
	}
}

// StatsUsers is the flat list of names the node should install as the v2ray_api
// allowlist after applying a users message. Traffic for a name absent from it
// is not counted at all.
func StatsUsers(byTag map[string][]singbox.User) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range byTag {
		for _, u := range list {
			if u.Name != "" && !seen[u.Name] {
				seen[u.Name] = true
				out = append(out, u.Name)
			}
		}
	}
	return out
}
