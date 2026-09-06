package service

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kosje/skysbx-panel/internal/store"
)

// relayFixture is two nodes: one that owns an inbound, one that could carry it.
func relayFixture(t *testing.T) (svc *Service, origin, relay int64) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	svc = New(st)
	o, _, err := svc.CreateNode("tokyo", "jp.example.com", "JP")
	if err != nil {
		t.Fatalf("create origin: %v", err)
	}
	r, _, err := svc.CreateNode("hongkong", "hk.example.com", "HK")
	if err != nil {
		t.Fatalf("create relay: %v", err)
	}
	return svc, o.ID, r.ID
}

func mustInbound(t *testing.T, svc *Service, nodeID int64, spec InboundSpec) *store.Inbound {
	t.Helper()
	in, err := svc.CreateInbound(nodeID, spec)
	if err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	return in
}

// The whole design in one test: the relay node gets a plain byte forwarder, and
// the origin node's configuration is untouched. If the origin ever stopped
// terminating the protocol itself, per-user traffic, address limits and the
// activity digest would all quietly collapse into one internal account — which
// is exactly the outcome relaying at layer 4 exists to avoid.
func TestRelayAddsAForwarderAndChangesNothingElse(t *testing.T) {
	svc, origin, relay := relayFixture(t)

	before, err := svc.NodeConfig(origin)
	if err != nil {
		t.Fatal(err)
	}
	in := mustInbound(t, svc, origin, InboundSpec{
		Protocol: store.ProtoVLESS, Port: 8443,
		RelayNodeID: relay, RelayPort: 443,
	})

	// The origin still serves the real inbound, on its own port.
	after, err := svc.NodeConfig(origin)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Inbounds) != len(before.Inbounds)+1 {
		t.Fatalf("origin has %d inbounds, want one more than %d",
			len(after.Inbounds), len(before.Inbounds))
	}
	real := after.Inbounds[len(after.Inbounds)-1]
	if real.Type != "vless" || real.ListenPort != 8443 {
		t.Errorf("origin inbound is %s on %d, want vless on 8443", real.Type, real.ListenPort)
	}
	if real.OverrideAddress != "" {
		t.Errorf("origin inbound became a forwarder: %+v", real)
	}

	// The relay gets a direct listener pointing at it, and nothing else.
	cfg, err := svc.NodeConfig(relay)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Inbounds) != 1 {
		t.Fatalf("relay has %d inbounds, want 1: %+v", len(cfg.Inbounds), cfg.Inbounds)
	}
	fwd := cfg.Inbounds[0]
	if fwd.Type != "direct" {
		t.Errorf("relay listener is %q, want direct — anything else decrypts", fwd.Type)
	}
	if fwd.ListenPort != 443 {
		t.Errorf("relay listens on %d, want 443", fwd.ListenPort)
	}
	if fwd.OverrideAddress != "jp.example.com" || fwd.OverridePort != 8443 {
		t.Errorf("relay forwards to %s:%d, want jp.example.com:8443",
			fwd.OverrideAddress, fwd.OverridePort)
	}
	if fwd.Tag != RelayTag(in.Tag) {
		t.Errorf("relay tag is %q, want %q", fwd.Tag, RelayTag(in.Tag))
	}
	// No credential of any kind crosses over. A key here would mean the relay
	// was terminating something, which is the whole thing being avoided.
	raw, _ := json.Marshal(fwd)
	for _, leak := range []string{"password", "private_key", "users", "tls"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("relay listener carries %q: %s", leak, raw)
		}
	}
}

// A relay listener has to bind. The node refuses a configuration it cannot
// bind, and that failure takes every other inbound on that node down with it —
// so a collision has to be caught here, not discovered by an outage.
func TestRelayPortMustBeFreeOnTheRelayNode(t *testing.T) {
	svc, origin, relay := relayFixture(t)
	mustInbound(t, svc, relay, InboundSpec{Protocol: store.ProtoVLESS, Port: 443})

	_, err := svc.CreateInbound(origin, InboundSpec{
		Protocol: store.ProtoShadowsocks, Port: 8388,
		RelayNodeID: relay, RelayPort: 443,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("relaying onto an occupied port was accepted: %v", err)
	}

	// And against another relay already using that port.
	mustInbound(t, svc, origin, InboundSpec{
		Protocol: store.ProtoShadowsocks, Port: 8388,
		RelayNodeID: relay, RelayPort: 8443,
	})
	_, err = svc.CreateInbound(origin, InboundSpec{
		Protocol: store.ProtoAnyTLS, Port: 9443, ServerName: "jp.example.com",
		RelayNodeID: relay, RelayPort: 8443,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("two relays on one port were accepted: %v", err)
	}
}

func TestRelayRejectsSelfAndLoops(t *testing.T) {
	svc, origin, _ := relayFixture(t)

	if _, err := svc.CreateInbound(origin, InboundSpec{
		Protocol: store.ProtoShadowsocks, Port: 8388,
		RelayNodeID: origin, RelayPort: 443,
	}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a node was allowed to relay its own inbound: %v", err)
	}

	// Two node records for one host: the only way a relay can forward to
	// itself, because a relay otherwise always targets a real inbound.
	twin, _, err := svc.CreateNode("tokyo2", "jp.example.com", "JP")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateInbound(origin, InboundSpec{
		Protocol: store.ProtoShadowsocks, Port: 8388,
		RelayNodeID: twin.ID, RelayPort: 8388,
	}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a relay pointing at its own address and port was accepted: %v", err)
	}
	// The same pair on a different port is fine: nothing forwards to itself.
	if _, err := svc.CreateInbound(origin, InboundSpec{
		Protocol: store.ProtoShadowsocks, Port: 8388,
		RelayNodeID: twin.ID, RelayPort: 443,
	}); err != nil {
		t.Errorf("a relay on a different port was rejected: %v", err)
	}
}

// Both fields answer "what do clients dial". Storing both would leave the
// subscription generator and the relay node's configuration reading different
// ones, and an inbound that resolves to two addresses cannot be debugged.
func TestRelayAndConnectAddressAreExclusive(t *testing.T) {
	svc, origin, relay := relayFixture(t)

	_, err := svc.CreateInbound(origin, InboundSpec{
		Protocol: store.ProtoShadowsocks, Port: 8388,
		Address: "relay.example.net:443", RelayNodeID: relay, RelayPort: 443,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("both relay forms at once were accepted: %v", err)
	}

	// Switching from one to the other has to clear the one being left behind.
	in := mustInbound(t, svc, origin, InboundSpec{
		Protocol: store.ProtoShadowsocks, Port: 8388,
		Address: "relay.example.net:443",
	})
	got, err := svc.EditInbound(in.ID, InboundEdit{
		Port: 8388, RelayNodeID: relay, RelayPort: 443,
	})
	if err != nil {
		t.Fatalf("switch to an in-panel relay: %v", err)
	}
	if got.Address != "" {
		t.Errorf("the external address survived the switch: %q", got.Address)
	}
	back, err := svc.EditInbound(in.ID, InboundEdit{Port: 8388})
	if err != nil {
		t.Fatalf("switch back: %v", err)
	}
	if back.RelayNodeID != 0 || back.RelayPort != 0 {
		t.Errorf("the relay survived being cleared: node %d port %d",
			back.RelayNodeID, back.RelayPort)
	}
}

// A relay for something that is not being served is an open port answering with
// a connection refused. Either end going away takes the listener with it.
func TestRelayListenerFollowsBothEnds(t *testing.T) {
	svc, origin, relay := relayFixture(t)
	in := mustInbound(t, svc, origin, InboundSpec{
		Protocol: store.ProtoShadowsocks, Port: 8388,
		RelayNodeID: relay, RelayPort: 443,
	})

	count := func() int {
		t.Helper()
		cfg, err := svc.NodeConfig(relay)
		if err != nil {
			t.Fatal(err)
		}
		return len(cfg.Inbounds)
	}
	if count() != 1 {
		t.Fatalf("relay listener missing to begin with")
	}

	if err := svc.SetInboundEnabled(in.ID, false); err != nil {
		t.Fatal(err)
	}
	if count() != 0 {
		t.Error("a disabled inbound left its relay listening")
	}
	if err := svc.SetInboundEnabled(in.ID, true); err != nil {
		t.Fatal(err)
	}

	node, _ := svc.Node(origin)
	node.Enabled = false
	if err := svc.UpdateNode(node); err != nil {
		t.Fatal(err)
	}
	if count() != 0 {
		t.Error("a disabled origin node left its relay listening")
	}
	node.Enabled = true
	if err := svc.UpdateNode(node); err != nil {
		t.Fatal(err)
	}

	if err := svc.DeleteInbound(in.ID); err != nil {
		t.Fatal(err)
	}
	if count() != 0 {
		t.Error("a deleted inbound left its relay listening")
	}
}

// A relay listener is built from the origin inbound's port and its node's
// address. Both live on a node the edit never mentions, so nothing else would
// tell the relay node that what it is forwarding to has moved.
func TestRelayHostIsToldWhenTheTargetMoves(t *testing.T) {
	svc, origin, relay := relayFixture(t)
	in := mustInbound(t, svc, origin, InboundSpec{
		Protocol: store.ProtoShadowsocks, Port: 8388,
		RelayNodeID: relay, RelayPort: 443,
	})

	spy := &spyNotifier{}
	svc.SetNotifier(spy)

	if _, err := svc.EditInbound(in.ID, InboundEdit{
		Port: 9388, RelayNodeID: relay, RelayPort: 443,
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(spy.config, relay) {
		t.Errorf("moving the origin port did not reach the relay node: %v", spy.config)
	}

	spy.config = nil
	node, _ := svc.Node(origin)
	node.Address = "jp2.example.com"
	if err := svc.UpdateNode(node); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(spy.config, relay) {
		t.Errorf("moving the origin address did not reach the relay node: %v", spy.config)
	}
	cfg, err := svc.NodeConfig(relay)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Inbounds[0].OverrideAddress != "jp2.example.com" ||
		cfg.Inbounds[0].OverridePort != 9388 {
		t.Errorf("relay still forwards to %s:%d",
			cfg.Inbounds[0].OverrideAddress, cfg.Inbounds[0].OverridePort)
	}

	// A rename rewrites the origin's tags, and the relay tag is derived from
	// them — so the relay node's listener is renamed by an edit to a different
	// node entirely.
	spy.config = nil
	node, _ = svc.Node(origin)
	node.Name = "osaka"
	if err := svc.UpdateNode(node); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(spy.config, relay) {
		t.Errorf("renaming the origin node did not reach the relay node: %v", spy.config)
	}
	cfg, _ = svc.NodeConfig(relay)
	renamed, _ := svc.Store().Inbound(in.ID)
	if cfg.Inbounds[0].Tag != RelayTag(renamed.Tag) {
		t.Errorf("relay tag is %q, want %q", cfg.Inbounds[0].Tag, RelayTag(renamed.Tag))
	}
}

// Deleting the relay node would otherwise return those inbounds to advertising
// their own node's address — the one thing the relay was set up to avoid, done
// without saying so.
func TestDeletingARelayNodeIsRefused(t *testing.T) {
	svc, origin, relay := relayFixture(t)
	in := mustInbound(t, svc, origin, InboundSpec{
		Protocol: store.ProtoShadowsocks, Port: 8388,
		RelayNodeID: relay, RelayPort: 443,
	})

	err := svc.DeleteNode(relay)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("deleting a node others relay through was allowed: %v", err)
	}
	// The message has to name what is in the way, or the operator has nothing
	// to act on.
	if !strings.Contains(err.Error(), in.Tag) {
		t.Errorf("the error does not name the inbound: %v", err)
	}

	// Clearing the relay releases the node.
	if _, err := svc.EditInbound(in.ID, InboundEdit{Port: 8388}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteNode(relay); err != nil {
		t.Fatalf("delete after clearing the relay: %v", err)
	}
}

// Deleting the *origin* node is fine, and has to take the relay listener with
// it: the inbound goes by cascade, and nothing else would tell the relay node.
func TestDeletingTheOriginNodeClearsTheRelay(t *testing.T) {
	svc, origin, relay := relayFixture(t)
	mustInbound(t, svc, origin, InboundSpec{
		Protocol: store.ProtoShadowsocks, Port: 8388,
		RelayNodeID: relay, RelayPort: 443,
	})

	spy := &spyNotifier{}
	svc.SetNotifier(spy)
	if err := svc.DeleteNode(origin); err != nil {
		t.Fatalf("delete origin: %v", err)
	}
	if !slices.Contains(spy.config, relay) {
		t.Errorf("the relay node was not told: %v", spy.config)
	}
	cfg, err := svc.NodeConfig(relay)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Inbounds) != 0 {
		t.Errorf("relay still listens for a deleted node: %+v", cfg.Inbounds)
	}
}

// The prefix names relay listeners. A hand-typed tag using it would shadow one,
// and the collision would only show up as a node refusing its configuration.
func TestReservedTagPrefixIsRefused(t *testing.T) {
	svc, origin, _ := relayFixture(t)
	if _, err := svc.CreateInbound(origin, InboundSpec{
		Protocol: store.ProtoShadowsocks, Tag: RelayTagPrefix + "mine", Port: 8388,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a tag using the reserved prefix was accepted: %v", err)
	}
}

// What passes through a relay is an already-encrypted proxy stream, so sniffing
// it finds the tunnel's own handshake and never what the client is doing inside
// it. The exemption has to be terminal and first, because sing-box has no
// "not this inbound" matcher — a rule below the sniffer would be too late.
func TestPolicySkipsRelayListeners(t *testing.T) {
	svc, origin, relay := relayFixture(t)
	mustInbound(t, svc, origin, InboundSpec{
		Protocol: store.ProtoShadowsocks, Port: 8388,
		RelayNodeID: relay, RelayPort: 443,
	})
	// The relay node also serves something of its own, which must still be
	// policed.
	own := mustInbound(t, svc, relay, InboundSpec{Protocol: store.ProtoVLESS, Port: 8443})

	if err := svc.SetPolicy(Policy{BlockBitTorrent: true}); err != nil {
		t.Fatal(err)
	}
	cfg, err := svc.NodeConfig(relay)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Route == nil || len(cfg.Route.Rules) < 3 {
		t.Fatalf("policy did not reach the relay node: %+v", cfg.Route)
	}
	first := cfg.Route.Rules[0]
	if first.Action != "route" || first.Outbound != "direct" {
		t.Fatalf("first rule is %q -> %q, want a terminal route to direct",
			first.Action, first.Outbound)
	}
	if !slices.Contains(first.Inbound, RelayTag("ss-tokyo")) {
		t.Errorf("the exemption does not name the relay listener: %v", first.Inbound)
	}
	if slices.Contains(first.Inbound, own.Tag) {
		t.Errorf("the exemption swallowed the relay node's own inbound %q", own.Tag)
	}
	if cfg.Route.Rules[1].Action != "sniff" {
		t.Errorf("second rule is %q, want the sniffer", cfg.Route.Rules[1].Action)
	}

	// A node carrying no relays gets no exemption rule at all: an empty inbound
	// list matches everything, which would disable the policy outright.
	origCfg, err := svc.NodeConfig(origin)
	if err != nil {
		t.Fatal(err)
	}
	if origCfg.Route == nil || origCfg.Route.Rules[0].Action != "sniff" {
		t.Errorf("a node with no relays got %+v, want the sniffer first", origCfg.Route)
	}
}
