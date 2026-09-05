package service

import (
	"testing"

	"github.com/kosje/skysbx-panel/internal/store"
)

// Every tag on the node follows the rename, including one that was typed by
// hand: a tag reading "01" on a node called osaka identifies neither the node
// nor the protocol, and it is the name that shows up in the node's logs, the
// panel's tables and the client's server list.
//
// Two inbounds of the same protocol get a numeric suffix, assigned in creation
// order so the same rename always produces the same tags.
func TestRenamingANodeRewritesEveryTag(t *testing.T) {
	svc, nodeID := editFixture(t)

	derived, err := svc.CreateInbound(nodeID, InboundSpec{
		Protocol: store.ProtoShadowsocks, Port: 8388})
	if err != nil {
		t.Fatalf("create derived: %v", err)
	}
	if derived.Tag != "ss-tokyo" {
		t.Fatalf("derived tag is %q, want ss-tokyo", derived.Tag)
	}
	chosen, err := svc.CreateInbound(nodeID, InboundSpec{
		Protocol: store.ProtoShadowsocks, Port: 8389, Tag: "01"})
	if err != nil {
		t.Fatalf("create chosen: %v", err)
	}

	n, err := svc.Node(nodeID)
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	n.Name = "osaka"
	if err := svc.UpdateNode(n); err != nil {
		t.Fatalf("rename: %v", err)
	}

	after, err := svc.NodeInbounds(nodeID)
	if err != nil {
		t.Fatalf("inbounds: %v", err)
	}
	tags := map[int64]string{}
	for _, in := range after {
		tags[in.ID] = in.Tag
	}
	if tags[derived.ID] != "ss-osaka" {
		t.Errorf("first tag is %q, want ss-osaka", tags[derived.ID])
	}
	if tags[chosen.ID] != "ss-osaka-2" {
		t.Errorf("second tag is %q, want ss-osaka-2", tags[chosen.ID])
	}
}

// The tag lives in the row the panel reads and in the sing-box object the node
// is sent. Leaving one behind means the node listens under a tag no user list
// mentions, and every user on that inbound silently stops authenticating.
func TestRenamingANodeRewritesTheTagInThePushedConfig(t *testing.T) {
	svc, nodeID := editFixture(t)

	in, err := svc.CreateInbound(nodeID, InboundSpec{
		Protocol: store.ProtoShadowsocks, Port: 8388})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.CreateUser(NewUser{Name: "alice"}); err != nil {
		t.Fatalf("create user: %v", err)
	}

	n, err := svc.Node(nodeID)
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	n.Name = "osaka"
	if err := svc.UpdateNode(n); err != nil {
		t.Fatalf("rename: %v", err)
	}

	cfg, err := svc.NodeConfig(nodeID)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if len(cfg.Inbounds) != 1 {
		t.Fatalf("got %d inbounds, want 1", len(cfg.Inbounds))
	}
	if cfg.Inbounds[0].Tag != "ss-osaka" {
		t.Errorf("pushed config tag is %q, want ss-osaka", cfg.Inbounds[0].Tag)
	}

	users, err := svc.NodeUsers(nodeID)
	if err != nil {
		t.Fatalf("users: %v", err)
	}
	if len(users["ss-osaka"]) != 1 {
		t.Errorf("user list is keyed by %v, not the new tag", keysOf(users))
	}
	if _, stale := users[in.Tag]; stale && in.Tag != "ss-osaka" {
		t.Error("the user list still carries the old tag")
	}
}

// Renaming onto a name whose derived tag is already taken has to give way
// rather than fail: refusing halfway would leave the node renamed and its
// inbounds inconsistent with it.
func TestRenamingIntoATagCollisionPicksAFreeTag(t *testing.T) {
	svc, nodeID := editFixture(t)

	derived, err := svc.CreateInbound(nodeID, InboundSpec{
		Protocol: store.ProtoShadowsocks, Port: 8388})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// A different node already holds the tag this rename is about to want.
	// Tags are globally unique because they address an inbound in a pushed
	// configuration, so the ones on other nodes are off limits.
	other, _, err := svc.CreateNode("paris", "fr.example.com", "FR")
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	if _, err := svc.CreateInbound(other.ID, InboundSpec{
		Protocol: store.ProtoShadowsocks, Port: 8388, Tag: "ss-osaka"}); err != nil {
		t.Fatalf("create squatter: %v", err)
	}

	n, err := svc.Node(nodeID)
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	n.Name = "osaka"
	if err := svc.UpdateNode(n); err != nil {
		t.Fatalf("rename: %v", err)
	}

	after, err := svc.Store().Inbound(derived.ID)
	if err != nil {
		t.Fatalf("inbound: %v", err)
	}
	if after.Tag != "ss-osaka-2" {
		t.Errorf("tag is %q, want ss-osaka-2", after.Tag)
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
