package service

import (
	"fmt"
	"strings"

	"github.com/kosje/skysbx-panel/internal/singbox"
	"github.com/kosje/skysbx-panel/internal/store"
)

// Relaying one node's inbound through another, at layer 4.
//
// The relay node runs a sing-box `direct` listener whose override_address and
// override_port point at the origin node's port. It copies bytes. It holds no
// key, terminates no protocol and has no idea which user is on the other end —
// which is exactly why this is worth doing and a chained proxy is not.
//
// Everything the panel counts keeps working because the origin node is still
// the one decrypting: per-user traffic, the address limit, the activity digest.
// A chained proxy would have to authenticate the entry node to the exit node as
// some internal account, and from the exit node's side every user would collapse
// into that one name.
//
// Two things are genuinely lost, and both are inherent rather than fixable here:
//
//   - The origin node sees the relay's address as the source, not the client's.
//     Address limits still apply per user, but a user connecting through a relay
//     from three places looks like one address. sing-box removed proxy_protocol
//     from inbounds in 1.13, so there is nowhere left to carry the real one.
//   - The relay node's own bandwidth is invisible to the traffic tables, which
//     count per user and a relay listener has no users. Its dashboard row reads
//     zero while its link is saturated.
//
// There is no cycle to detect. A relay always forwards to a real inbound, never
// to another relay, so relays cannot chain into a loop — the one exception being
// two node records that happen to share an address, which checkRelay rejects.

// RelayTagPrefix names the listener a relay node runs on another node's behalf.
// Inbound tags are globally unique and always begin with a protocol slug, so a
// derived tag can never collide with one; a hand-typed tag could, which is why
// CreateInbound refuses this prefix.
const RelayTagPrefix = "relay-"

// RelayTag is the tag of the listener that carries origin on its relay node.
// Derived rather than stored so the two can never drift, and readable in the
// relay node's logs, which is where an operator will actually meet it.
func RelayTag(originTag string) string { return RelayTagPrefix + originTag }

// relayListener is the sing-box inbound a relay node runs for one origin
// inbound. Nothing protocol-specific appears in it: the relay does not know or
// care what it is carrying.
func relayListener(in *store.Inbound, originAddress string) singbox.Inbound {
	return singbox.Inbound{
		Type:            "direct",
		Tag:             RelayTag(in.Tag),
		Listen:          "::",
		ListenPort:      in.RelayPort,
		OverrideAddress: originAddress,
		OverridePort:    in.Port,
	}
}

// checkRelay validates a relay assignment for one inbound.
//
// originNodeID and port describe the inbound as it will be after the edit, not
// as it is stored: an edit can move the port and set the relay in one save.
// exceptID is the inbound being edited, excluded from the port collision check
// so that re-saving a form without changing anything is not a conflict.
func (s *Service) checkRelay(originNodeID, relayNodeID int64, port, relayPort int,
	exceptID int64,
) error {
	if relayNodeID == originNodeID {
		return invalid("a node cannot relay its own inbound")
	}
	relayNode, err := s.st.Node(relayNodeID)
	if err == store.ErrNotFound {
		return invalid("no such relay node")
	} else if err != nil {
		return err
	}
	if relayPort < 1 || relayPort > 65535 {
		return invalid("relay port %d out of range", relayPort)
	}

	originNode, err := s.st.Node(originNodeID)
	if err != nil {
		return err
	}
	// Two node records pointing at the same host is the one way a relay can end
	// up forwarding to itself. It is a strange configuration to arrive at, and
	// an infinite loop on a live port is a strange way to find out.
	if strings.EqualFold(strings.TrimSpace(relayNode.Address),
		strings.TrimSpace(originNode.Address)) && relayPort == port {
		return invalid("relay node %s has the same address as %s, so port %d "+
			"would forward to itself", relayNode.Name, originNode.Name, relayPort)
	}

	// The relay listener has to bind, and the node will refuse the whole
	// configuration if it cannot — taking every other inbound on that node down
	// with it. Cheaper to say so here.
	own, err := s.st.NodeInbounds(relayNodeID)
	if err != nil {
		return err
	}
	for _, other := range own {
		if other.Enabled && other.Port == relayPort {
			return invalid("%s already listens on port %d (inbound %s)",
				relayNode.Name, relayPort, other.Tag)
		}
	}
	carried, err := s.st.InboundsRelayedVia(relayNodeID)
	if err != nil {
		return err
	}
	for _, other := range carried {
		if other.ID != exceptID && other.Enabled && other.RelayPort == relayPort {
			return invalid("%s already relays port %d for inbound %s",
				relayNode.Name, relayPort, other.Tag)
		}
	}
	return nil
}

// relayInbounds is the set of listeners a node runs on other nodes' behalf,
// plus their tags so the routing policy can leave them alone.
func (s *Service) relayInbounds(relayNodeID int64) ([]singbox.Inbound, error) {
	carried, err := s.st.InboundsRelayedVia(relayNodeID)
	if err != nil {
		return nil, err
	}
	if len(carried) == 0 {
		return nil, nil
	}
	nodes, err := s.st.Nodes()
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*store.Node, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}

	out := make([]singbox.Inbound, 0, len(carried))
	for _, in := range carried {
		// A relay for something that is not being served would be a port open to
		// a connection refused. Disabling either end takes the listener down.
		if !in.Enabled {
			continue
		}
		origin, ok := byID[in.NodeID]
		if !ok || !origin.Enabled {
			continue
		}
		out = append(out, relayListener(in, origin.Address))
	}
	return out, nil
}

// notifyRelayHost tells the node carrying this inbound that its listener has
// changed. Separate from the origin node's own push: a relay listener is built
// from the origin inbound's port and its node's address, so an edit to either
// changes a configuration on a node the edit never mentioned.
func (s *Service) notifyRelayHost(relayNodeID int64) {
	if relayNodeID != 0 {
		s.notify.ConfigChanged(relayNodeID)
	}
}

// notifyRelayHostsOf pushes to every node carrying a relay for any inbound on
// the given node. Used when something node-wide moves — its address, its name
// (which retags its inbounds, and relay tags are derived from those), or whether
// it is enabled at all.
func (s *Service) notifyRelayHostsOf(originNodeID int64) error {
	inbounds, err := s.st.NodeInbounds(originNodeID)
	if err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, in := range inbounds {
		if in.RelayNodeID != 0 && !seen[in.RelayNodeID] {
			seen[in.RelayNodeID] = true
			s.notify.ConfigChanged(in.RelayNodeID)
		}
	}
	return nil
}

// RelayedVia is one inbound another node has asked this node to carry, named
// well enough to show without the caller joining anything.
type RelayedVia struct {
	Inbound    *store.Inbound
	OriginNode string
	ListenPort int
	Target     string // where the bytes go, as the node will dial it
	Tag        string // the listener's tag on this node
	Serving    bool   // false when either end is disabled, so nothing listens
}

// RelaysVia lists what a node carries for others, so its inbound page can
// account for the ports it is holding. Without it those ports are open, absent
// from every table, and a mystery.
func (s *Service) RelaysVia(nodeID int64) ([]RelayedVia, error) {
	carried, err := s.st.InboundsRelayedVia(nodeID)
	if err != nil {
		return nil, err
	}
	if len(carried) == 0 {
		return nil, nil
	}
	nodes, err := s.st.Nodes()
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*store.Node, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	relayNode := byID[nodeID]

	out := make([]RelayedVia, 0, len(carried))
	for _, in := range carried {
		origin := byID[in.NodeID]
		r := RelayedVia{
			Inbound: in, ListenPort: in.RelayPort, Tag: RelayTag(in.Tag),
			Serving: in.Enabled && origin != nil && origin.Enabled &&
				relayNode != nil && relayNode.Enabled,
		}
		if origin != nil {
			r.OriginNode = origin.Name
			r.Target = fmt.Sprintf("%s:%d", origin.Address, in.Port)
		}
		out = append(out, r)
	}
	return out, nil
}

// RelayCandidates is every node that could carry this one's inbounds: all of
// them but itself.
func (s *Service) RelayCandidates(nodeID int64) ([]*store.Node, error) {
	nodes, err := s.st.Nodes()
	if err != nil {
		return nil, err
	}
	out := make([]*store.Node, 0, len(nodes))
	for _, n := range nodes {
		if n.ID != nodeID {
			out = append(out, n)
		}
	}
	return out, nil
}
