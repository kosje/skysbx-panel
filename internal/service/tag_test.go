package service

import (
	"path/filepath"
	"testing"

	"github.com/kosje/skysb-panel/internal/store"
)

func newSvc(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st), st
}

func mustNode(t *testing.T, svc *Service, name string) int64 {
	t.Helper()
	n, _, err := svc.CreateNode(name, "example.com", "XX")
	if err != nil {
		t.Fatalf("create node %q: %v", name, err)
	}
	return n.ID
}

// An inbound tag is globally unique and addresses the inbound in the config
// pushed to a node. Requiring the operator to invent one is a collision waiting
// to happen, so an empty tag has to be filled in for them.
func TestInboundTagIsDerived(t *testing.T) {
	svc, _ := newSvc(t)
	id := mustNode(t, svc, "tokyo")

	cases := []struct {
		protocol string
		want     string
		spec     InboundSpec
	}{
		{store.ProtoVLESS, "vless-tokyo", InboundSpec{Port: 443, Handshake: DefaultHandshake}},
		{store.ProtoAnyTLS, "anytls-tokyo", InboundSpec{Port: 8443,
			CertPath: "/c.pem", KeyPath: "/k.pem", ServerName: "jp.example.com"}},
		{store.ProtoShadowsocks, "ss-tokyo", InboundSpec{Port: 8388}},
	}
	for _, c := range cases {
		c.spec.Protocol = c.protocol
		in, err := svc.CreateInbound(id, c.spec)
		if err != nil {
			t.Fatalf("%s: %v", c.protocol, err)
		}
		if in.Tag != c.want {
			t.Errorf("%s: tag = %q, want %q", c.protocol, in.Tag, c.want)
		}
	}
}

// A second inbound of the same protocol on the same node must not collide with
// the first; the unique index would reject it and the operator would see a
// database error for something the panel can resolve itself.
func TestDerivedTagAvoidsCollision(t *testing.T) {
	svc, _ := newSvc(t)
	id := mustNode(t, svc, "tokyo")

	first, err := svc.CreateInbound(id, InboundSpec{
		Protocol: store.ProtoVLESS, Port: 443, Handshake: DefaultHandshake})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := svc.CreateInbound(id, InboundSpec{
		Protocol: store.ProtoVLESS, Port: 8443, Handshake: DefaultHandshake})
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	if first.Tag != "vless-tokyo" || second.Tag != "vless-tokyo-2" {
		t.Fatalf("tags = %q, %q; want vless-tokyo, vless-tokyo-2", first.Tag, second.Tag)
	}
}

// Names that survive node-name validation still have to be lowered and mapped
// onto a tag.
func TestDerivedTagFromValidNodeNames(t *testing.T) {
	cases := []struct{ node, want string }{
		{"Tokyo", "vless-tokyo"},
		{"jp-west_1", "vless-jp-west_1"},
		{"NRT.01", "vless-nrt01"}, // dots are legal in a name, not in a tag
	}
	for _, c := range cases {
		svc, _ := newSvc(t)
		id := mustNode(t, svc, c.node)
		in, err := svc.CreateInbound(id, InboundSpec{
			Protocol: store.ProtoVLESS, Port: 443, Handshake: DefaultHandshake})
		if err != nil {
			t.Fatalf("node %q: %v", c.node, err)
		}
		if in.Tag != c.want {
			t.Errorf("node %q: tag = %q, want %q", c.node, in.Tag, c.want)
		}
	}
}

// Derivation is kept total on its own, independent of what node-name validation
// happens to allow today. If that rule is ever loosened — a non-ASCII node name
// is a reasonable thing to want — this is what stops it producing an empty tag.
func TestDeriveInboundTagIsTotal(t *testing.T) {
	svc, _ := newSvc(t)
	cases := []struct{ name, want string }{
		{"Tokyo #2", "vless-tokyo2"},
		{"东京", "vless-node"}, // nothing tag-safe survives
		{"", "vless-node"},
	}
	for _, c := range cases {
		if got := svc.deriveInboundTag(store.ProtoVLESS, c.name); got != c.want {
			t.Errorf("deriveInboundTag(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

// An explicit tag stays an override.
func TestExplicitTagWins(t *testing.T) {
	svc, _ := newSvc(t)
	id := mustNode(t, svc, "tokyo")

	in, err := svc.CreateInbound(id, InboundSpec{
		Protocol: store.ProtoVLESS, Tag: "custom-name", Port: 443,
		Handshake: DefaultHandshake})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if in.Tag != "custom-name" {
		t.Fatalf("tag = %q, want custom-name", in.Tag)
	}
}
