package service

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kosje/skysbx-panel/internal/store"
)

// A node may only bill the users it was given.
//
// Without this, any node can charge traffic to any account on the panel — and
// since crossing a limit revokes access, one compromised node can disable every
// other node's users by reporting a large enough number for each of them.
func TestANodeCannotBillAUserItDoesNotServe(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := New(st)

	tokyo, _, _ := svc.CreateNode("tokyo", "jp.example.com", "JP")
	paris, _, _ := svc.CreateNode("paris", "fr.example.com", "FR")
	inTokyo, err := svc.CreateInbound(tokyo.ID, InboundSpec{
		Protocol: store.ProtoShadowsocks, Port: 8388})
	if err != nil {
		t.Fatal(err)
	}
	inParis, err := svc.CreateInbound(paris.ID, InboundSpec{
		Protocol: store.ProtoShadowsocks, Port: 8388})
	if err != nil {
		t.Fatal(err)
	}

	alice, _ := svc.CreateUser(NewUser{Name: "alice"})
	bob, _ := svc.CreateUser(NewUser{Name: "bob"})
	// alice only on tokyo, bob only on paris.
	if err := svc.SetUserInbounds(alice.ID, []int64{inTokyo.ID}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetUserInbounds(bob.ID, []int64{inParis.ID}); err != nil {
		t.Fatal(err)
	}

	// Paris reports for both. Only bob's may land.
	if err := svc.RecordTraffic(paris.ID, map[string]Usage{
		"alice": {Up: 1, Down: 1 << 20},
		"bob":   {Up: 1, Down: 2 << 20},
	}); err != nil {
		t.Fatal(err)
	}

	gotAlice, _ := svc.User(alice.ID)
	gotBob, _ := svc.User(bob.ID)
	if gotAlice.TrafficUsed != 0 {
		t.Errorf("paris billed %d bytes to a user it does not serve", gotAlice.TrafficUsed)
	}
	if gotBob.TrafficUsed == 0 {
		t.Error("paris could not bill its own user")
	}

	// An unrestricted user is on every node, so every node may bill them.
	carol, _ := svc.CreateUser(NewUser{Name: "carol"})
	if err := svc.RecordTraffic(paris.ID, map[string]Usage{
		"carol": {Up: 1, Down: 1 << 20}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.User(carol.ID); got.TrafficUsed == 0 {
		t.Error("a user with no inbound restrictions was not billable")
	}
}

// A single interval is thirty seconds. A figure that could not have been
// measured is not a measurement, and unlike a wrong small number a wrong
// enormous one permanently revokes an account.
func TestAbsurdTrafficDeltasAreRejected(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := New(st)

	node, _, _ := svc.CreateNode("tokyo", "jp.example.com", "JP")
	// The node has to serve something, or it may not bill anyone at all and
	// this test would pass for the wrong reason.
	if _, err := svc.CreateInbound(node.ID, InboundSpec{
		Protocol: store.ProtoShadowsocks, Port: 8388}); err != nil {
		t.Fatal(err)
	}
	u, _ := svc.CreateUser(NewUser{Name: "alice", TrafficLimit: 10 << 30})

	if err := svc.RecordTraffic(node.ID, map[string]Usage{
		"alice": {Up: 0, Down: 1 << 62},
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.User(u.ID)
	if got.TrafficUsed != 0 {
		t.Fatalf("an impossible delta was accepted: %d bytes", got.TrafficUsed)
	}
	if !got.Active(time.Now()) {
		t.Error("one bogus frame revoked the account")
	}

	// A plausible one still lands, so the cap is not simply refusing everything.
	if err := svc.RecordTraffic(node.ID, map[string]Usage{
		"alice": {Up: 1 << 20, Down: 1 << 30},
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.User(u.ID); got.TrafficUsed != (1<<20)+(1<<30) {
		t.Errorf("a normal report did not land: %d", got.TrafficUsed)
	}
}

// A node with no enabled inbounds authenticates nobody, so it has nothing to
// report — and yet an unrestricted user is "on every node", which the first
// version of the scoping read as "billable by every node". A relay-only node,
// or one that has been emptied out, could charge every unrestricted account.
func TestANodeWithNoInboundsCannotBillAnyone(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := New(st)

	empty, _, _ := svc.CreateNode("relaybox", "hk.example.com", "HK")
	u, _ := svc.CreateUser(NewUser{Name: "carol"}) // unrestricted

	if err := svc.RecordTraffic(empty.ID, map[string]Usage{
		"carol": {Up: 1, Down: 1 << 30}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.User(u.ID); got.TrafficUsed != 0 {
		t.Errorf("a node with no inbounds billed %d bytes", got.TrafficUsed)
	}

	// A disabled inbound does not count either: nothing is listening on it.
	in, err := svc.CreateInbound(empty.ID, InboundSpec{Protocol: store.ProtoShadowsocks, Port: 8388})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetInboundEnabled(in.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordTraffic(empty.ID, map[string]Usage{
		"carol": {Up: 1, Down: 1 << 30}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.User(u.ID); got.TrafficUsed != 0 {
		t.Errorf("a node with only a disabled inbound billed %d bytes", got.TrafficUsed)
	}

	// Enable it and the same report lands.
	if err := svc.SetInboundEnabled(in.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordTraffic(empty.ID, map[string]Usage{
		"carol": {Up: 1, Down: 1 << 30}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.User(u.ID); got.TrafficUsed == 0 {
		t.Error("a node with a live inbound could not bill its unrestricted user")
	}
}
