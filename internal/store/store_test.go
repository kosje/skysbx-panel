package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrateIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	s.Close()

	// Re-opening must not try to re-apply migrations. This is the failure mode
	// that only shows up on the second start, i.e. in production.
	s, err = Open(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer s.Close()

	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 applied migration, got %d", n)
	}
}

func TestUserRoundTrip(t *testing.T) {
	s := openTemp(t)

	exp := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	u := &User{
		Name: "alice", VlessUUID: "uuid-1", Password: "pw", SSPassword: "psk",
		SubToken: "tok-1", Enabled: true, ExpiresAt: &exp, TrafficLimit: 1 << 30,
	}
	if err := s.CreateUser(u); err != nil {
		t.Fatalf("create: %v", err)
	}
	if u.ID == 0 {
		t.Fatal("CreateUser did not set ID")
	}

	got, err := s.User(u.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Name != "alice" || got.TrafficLimit != 1<<30 {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) {
		t.Fatalf("expires_at round trip: got %v want %v", got.ExpiresAt, exp)
	}

	byToken, err := s.UserBySubToken("tok-1")
	if err != nil || byToken.ID != u.ID {
		t.Fatalf("UserBySubToken: %v %+v", err, byToken)
	}

	if _, err := s.UserBySubToken("nope"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// UpdateUser must not touch traffic_used: the UI and the traffic reporter both
// write to a user row, and an edit that reset someone's usage to a stale value
// would be silent and unrecoverable.
func TestUpdateUserLeavesTrafficAlone(t *testing.T) {
	s := openTemp(t)

	u := &User{Name: "bob", VlessUUID: "u", Password: "p", SSPassword: "s",
		SubToken: "t", Enabled: true}
	if err := s.CreateUser(u); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.DB().Exec(`UPDATE users SET traffic_used = 12345 WHERE id = ?`, u.ID); err != nil {
		t.Fatalf("seed traffic: %v", err)
	}

	u.Note = "edited"
	u.TrafficUsed = 0 // a stale value carried in from a form
	if err := s.UpdateUser(u); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, _ := s.User(u.ID)
	if got.TrafficUsed != 12345 {
		t.Fatalf("traffic_used was clobbered: got %d want 12345", got.TrafficUsed)
	}
	if got.Note != "edited" {
		t.Fatalf("note not updated: %q", got.Note)
	}
}

func TestUserActive(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	cases := []struct {
		name string
		u    User
		want bool
	}{
		{"enabled, no limits", User{Enabled: true}, true},
		{"disabled", User{Enabled: false}, false},
		{"expired", User{Enabled: true, ExpiresAt: &past}, false},
		{"not yet expired", User{Enabled: true, ExpiresAt: &future}, true},
		{"under limit", User{Enabled: true, TrafficLimit: 100, TrafficUsed: 99}, true},
		{"at limit", User{Enabled: true, TrafficLimit: 100, TrafficUsed: 100}, false},
		{"over limit", User{Enabled: true, TrafficLimit: 100, TrafficUsed: 101}, false},
		{"unlimited", User{Enabled: true, TrafficLimit: 0, TrafficUsed: 1 << 40}, true},
	}
	for _, c := range cases {
		if got := c.u.Active(now); got != c.want {
			t.Errorf("%s: Active() = %v, want %v", c.name, got, c.want)
		}
	}
}

// Deleting a node must take its inbounds with it, or the subscription
// generator would emit entries pointing at a node that no longer exists.
func TestDeleteNodeCascadesToInbounds(t *testing.T) {
	s := openTemp(t)

	n := &Node{Name: "tokyo", TokenHash: "h", Address: "jp.example.com", Country: "JP", Enabled: true}
	if err := s.CreateNode(n); err != nil {
		t.Fatalf("create node: %v", err)
	}
	in := &Inbound{NodeID: n.ID, Tag: "vless-tokyo", Protocol: ProtoVLESS,
		Port: 443, Config: "{}", Client: "{}", Enabled: true}
	if err := s.CreateInbound(in); err != nil {
		t.Fatalf("create inbound: %v", err)
	}

	if err := s.DeleteNode(n.ID); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	if _, err := s.Inbound(in.ID); err != ErrNotFound {
		t.Fatalf("inbound survived node deletion: %v", err)
	}
}

func TestUserInbounds(t *testing.T) {
	s := openTemp(t)

	n := &Node{Name: "n1", TokenHash: "h", Address: "a", Enabled: true}
	if err := s.CreateNode(n); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, tag := range []string{"a", "b", "c"} {
		in := &Inbound{NodeID: n.ID, Tag: tag, Protocol: ProtoVLESS, Port: 443,
			Config: "{}", Client: "{}", Enabled: true}
		if err := s.CreateInbound(in); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, in.ID)
	}
	u := &User{Name: "u", VlessUUID: "x", Password: "p", SSPassword: "s",
		SubToken: "st", Enabled: true}
	if err := s.CreateUser(u); err != nil {
		t.Fatal(err)
	}

	// No rows means unrestricted, not "no access".
	got, err := s.UserInboundIDs(u.ID)
	if err != nil || len(got) != 0 {
		t.Fatalf("expected no restrictions: %v %v", got, err)
	}

	if err := s.SetUserInbounds(u.ID, ids[:2]); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, _ = s.UserInboundIDs(u.ID)
	if len(got) != 2 {
		t.Fatalf("expected 2 restrictions, got %v", got)
	}

	// Setting replaces rather than appends.
	if err := s.SetUserInbounds(u.ID, ids[2:]); err != nil {
		t.Fatalf("re-set: %v", err)
	}
	got, _ = s.UserInboundIDs(u.ID)
	if len(got) != 1 || got[0] != ids[2] {
		t.Fatalf("expected replacement, got %v", got)
	}
}

func TestSettings(t *testing.T) {
	s := openTemp(t)

	if v, err := s.Setting("missing"); err != nil || v != "" {
		t.Fatalf("missing setting should be empty: %q %v", v, err)
	}
	if err := s.SetSetting("k", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting("k", "v2"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if v, _ := s.Setting("k"); v != "v2" {
		t.Fatalf("expected v2, got %q", v)
	}
}

// A duplicate name has to be distinguishable from a real failure, or the UI can
// only ever say "something went wrong" to a problem the operator can fix.
func TestDuplicatesReportConflict(t *testing.T) {
	s := openTemp(t)

	u := &User{Name: "alice", VlessUUID: "u", Password: "p", SSPassword: "s",
		SubToken: "t1", Enabled: true}
	if err := s.CreateUser(u); err != nil {
		t.Fatal(err)
	}
	dup := &User{Name: "alice", VlessUUID: "u2", Password: "p2", SSPassword: "s2",
		SubToken: "t2", Enabled: true}
	if err := s.CreateUser(dup); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name should be ErrConflict, got %v", err)
	}

	// A duplicate subscription token matters more: it would hand one user's
	// configs to another.
	dup2 := &User{Name: "bob", VlessUUID: "u3", Password: "p3", SSPassword: "s3",
		SubToken: "t1", Enabled: true}
	if err := s.CreateUser(dup2); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate sub token should be ErrConflict, got %v", err)
	}

	n := &Node{Name: "tokyo", TokenHash: "h", Address: "a", Enabled: true}
	if err := s.CreateNode(n); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(&Node{Name: "tokyo", TokenHash: "h2", Address: "b",
		Enabled: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate node name should be ErrConflict, got %v", err)
	}

	in := &Inbound{NodeID: n.ID, Tag: "vless", Protocol: ProtoVLESS, Port: 443,
		Config: "{}", Client: "{}", Enabled: true}
	if err := s.CreateInbound(in); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateInbound(&Inbound{NodeID: n.ID, Tag: "vless",
		Protocol: ProtoVLESS, Port: 444, Config: "{}", Client: "{}",
		Enabled: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate inbound tag should be ErrConflict, got %v", err)
	}
}
