package service

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/kosje/skysb-panel/internal/store"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st)
}

type spyNotifier struct {
	users  int
	config []int64
}

func (s *spyNotifier) UsersChanged()          { s.users++ }
func (s *spyNotifier) ConfigChanged(id int64) { s.config = append(s.config, id) }

func TestCreateUserGeneratesCredentials(t *testing.T) {
	svc := newTestService(t)

	a, err := svc.CreateUser(NewUser{Name: "alice"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	b, err := svc.CreateUser(NewUser{Name: "bob"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	for _, f := range []struct{ name, va, vb string }{
		{"vless uuid", a.VlessUUID, b.VlessUUID},
		{"password", a.Password, b.Password},
		{"ss password", a.SSPassword, b.SSPassword},
		{"sub token", a.SubToken, b.SubToken},
	} {
		if f.va == "" {
			t.Errorf("%s was not generated", f.name)
		}
		if f.va == f.vb {
			t.Errorf("%s is shared between two users: %q", f.name, f.va)
		}
	}
}

func TestUserNameValidation(t *testing.T) {
	svc := newTestService(t)

	bad := []string{"", " ", "-leading", ".leading", "has space", "has/slash",
		"has:colon", "quote\"", "verylongnameverylongnameverylongname"}
	for _, name := range bad {
		if _, err := svc.CreateUser(NewUser{Name: name}); !errors.Is(err, ErrInvalid) {
			t.Errorf("name %q should have been rejected, got %v", name, err)
		}
	}

	good := []string{"a", "alice", "alice.smith", "alice-1", "alice_1", "A1"}
	for _, name := range good {
		if _, err := svc.CreateUser(NewUser{Name: name}); err != nil {
			t.Errorf("name %q should have been accepted: %v", name, err)
		}
	}
}

func TestDuplicateUserNameRejected(t *testing.T) {
	svc := newTestService(t)
	if _, err := svc.CreateUser(NewUser{Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateUser(NewUser{Name: "alice"}); err == nil {
		t.Fatal("duplicate user name should have been rejected")
	}
}

func TestNodeTokenAuthenticates(t *testing.T) {
	svc := newTestService(t)

	n, token, err := svc.CreateNode("tokyo", "jp.example.com", "jp")
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	if token == "" {
		t.Fatal("no token returned")
	}
	if n.Country != "JP" {
		t.Fatalf("country should be upper-cased, got %q", n.Country)
	}
	// The plaintext must not be recoverable from what was stored.
	if n.TokenHash == token {
		t.Fatal("token was stored in plaintext")
	}

	id, err := svc.AuthenticateNode(token)
	if err != nil || id != n.ID {
		t.Fatalf("authenticate: id=%d err=%v", id, err)
	}

	if _, err := svc.AuthenticateNode(token + "x"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("a wrong token should be rejected, got %v", err)
	}
	if _, err := svc.AuthenticateNode(""); !errors.Is(err, ErrBadCredentials) {
		t.Fatal("an empty token should be rejected")
	}
}

// A disabled node must not be able to connect, or disabling it in the UI would
// stop it appearing in subscriptions while leaving it serving traffic.
func TestDisabledNodeCannotAuthenticate(t *testing.T) {
	svc := newTestService(t)

	n, token, err := svc.CreateNode("tokyo", "jp.example.com", "JP")
	if err != nil {
		t.Fatal(err)
	}
	n.Enabled = false
	if err := svc.UpdateNode(n); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateNode(token); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("disabled node authenticated: %v", err)
	}
}

func TestRotateNodeTokenInvalidatesOld(t *testing.T) {
	svc := newTestService(t)

	n, old, err := svc.CreateNode("tokyo", "jp.example.com", "JP")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := svc.RotateNodeToken(n.ID)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if fresh == old {
		t.Fatal("rotation produced the same token")
	}
	if _, err := svc.AuthenticateNode(old); !errors.Is(err, ErrBadCredentials) {
		t.Fatal("the old token still works after rotation")
	}
	if id, err := svc.AuthenticateNode(fresh); err != nil || id != n.ID {
		t.Fatalf("the new token does not work: %v", err)
	}
}

func TestAdminCredentials(t *testing.T) {
	svc := newTestService(t)

	if ok, _ := svc.AdminExists(); ok {
		t.Fatal("a fresh database should have no admin")
	}
	if err := svc.CheckAdmin("admin", "whatever"); !errors.Is(err, ErrBadCredentials) {
		t.Fatal("login should fail before setup")
	}

	if err := svc.SetAdmin("admin", "short"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a short password should be rejected, got %v", err)
	}
	// bcrypt truncates past 72 bytes, so anything longer would not really be
	// checked. Refusing is the honest behaviour.
	long := make([]byte, 100)
	for i := range long {
		long[i] = 'a'
	}
	if err := svc.SetAdmin("admin", string(long)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an over-long password should be rejected, got %v", err)
	}

	if err := svc.SetAdmin("admin", "correct horse battery"); err != nil {
		t.Fatalf("set admin: %v", err)
	}
	if ok, _ := svc.AdminExists(); !ok {
		t.Fatal("AdminExists should now be true")
	}
	if err := svc.CheckAdmin("admin", "correct horse battery"); err != nil {
		t.Fatalf("correct login rejected: %v", err)
	}
	if err := svc.CheckAdmin("admin", "wrong"); !errors.Is(err, ErrBadCredentials) {
		t.Fatal("wrong password accepted")
	}
	if err := svc.CheckAdmin("root", "correct horse battery"); !errors.Is(err, ErrBadCredentials) {
		t.Fatal("wrong username accepted")
	}
}

// The hub relies on these callbacks to know when to push. A missing
// notification is invisible until a user cannot connect, so assert on them.
func TestNotifications(t *testing.T) {
	svc := newTestService(t)
	spy := &spyNotifier{}
	svc.SetNotifier(spy)

	u, err := svc.CreateUser(NewUser{Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if spy.users != 1 {
		t.Fatalf("CreateUser should notify, got %d", spy.users)
	}

	u.Note = "x"
	if err := svc.UpdateUser(u); err != nil {
		t.Fatal(err)
	}
	if err := svc.ResetUserTraffic(u.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteUser(u.ID); err != nil {
		t.Fatal(err)
	}
	if spy.users != 4 {
		t.Fatalf("expected 4 user notifications, got %d", spy.users)
	}

	n, _, err := svc.CreateNode("tokyo", "jp.example.com", "JP")
	if err != nil {
		t.Fatal(err)
	}
	// Creating a node alone changes nothing the node is running.
	if len(spy.config) != 0 {
		t.Fatalf("CreateNode should not push config, got %v", spy.config)
	}

	in, err := svc.CreateInbound(n.ID, InboundSpec{
		Protocol: store.ProtoVLESS, Tag: "vless-tokyo", Port: 443, Handshake: "example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetInboundEnabled(in.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteInbound(in.ID); err != nil {
		t.Fatal(err)
	}
	if len(spy.config) != 3 {
		t.Fatalf("expected 3 config notifications, got %v", spy.config)
	}
	for _, id := range spy.config {
		if id != n.ID {
			t.Fatalf("config notification carried node %d, want %d", id, n.ID)
		}
	}
}

func TestCreateInboundRejectsUnknownNode(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.CreateInbound(999, InboundSpec{
		Protocol: store.ProtoVLESS, Tag: "t", Port: 443, Handshake: "example.com",
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
