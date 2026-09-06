package service

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/kosje/skysbx-panel/internal/store"
)

func authFixture(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st)
}

// The reason the token index exists. Authentication used to bcrypt every
// enabled node's hash against whatever was presented, on an endpoint that by
// definition has no credential yet — a measured second of CPU per request on a
// twenty-node panel, for a token of any old garbage.
//
// The bound here is deliberately loose. What matters is the shape: the cost of
// a wrong token must not grow with the number of nodes, and must not include a
// bcrypt at all.
func TestWrongNodeTokenIsCheap(t *testing.T) {
	svc := authFixture(t)
	for i := range 20 {
		if _, _, err := svc.CreateNode(
			"node"+string(rune('a'+i)), "x.example", "US"); err != nil {
			t.Fatal(err)
		}
	}

	// Would run the slow path if there were one; there must not be.
	slowRan := false
	start := time.Now()
	const tries = 50
	for range tries {
		if _, err := svc.AuthenticateNode("not-a-real-token",
			func() bool { slowRan = true; return true }); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("a garbage token was not rejected: %v", err)
		}
	}
	per := time.Since(start) / tries

	if slowRan {
		t.Error("a wrong token still reached the bcrypt scan")
	}
	// One bcrypt is ~50ms. Fifty rejections finishing in the time a single
	// bcrypt would take is the whole point.
	if per > 5*time.Millisecond {
		t.Errorf("a wrong token costs %v; it should be an index probe", per)
	}
	t.Logf("20 nodes, wrong token: %v per attempt", per)
}

func TestNodeTokenRoundTrip(t *testing.T) {
	svc := authFixture(t)
	n, token, err := svc.CreateNode("tokyo", "jp.example.com", "JP")
	if err != nil {
		t.Fatal(err)
	}

	id, err := svc.AuthenticateNode(token, nil)
	if err != nil || id != n.ID {
		t.Fatalf("AuthenticateNode = %d, %v; want %d", id, err, n.ID)
	}
	// Rotating has to move the index with the hash, or the old token keeps
	// working and the new one never does.
	fresh, err := svc.RotateNodeToken(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateNode(token, nil); !errors.Is(err, ErrBadCredentials) {
		t.Error("the old token still authenticates after a rotation")
	}
	if id, err := svc.AuthenticateNode(fresh, nil); err != nil || id != n.ID {
		t.Errorf("the new token does not authenticate: %d %v", id, err)
	}

	// A disabled node is not a node that can connect.
	n.Enabled = false
	if err := svc.UpdateNode(n); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateNode(fresh, nil); !errors.Is(err, ErrBadCredentials) {
		t.Error("a disabled node authenticated")
	}
}

// A node created before the index has only a bcrypt hash, and the plaintext is
// gone — so it cannot be backfilled and has to keep working through the scan.
// It must also upgrade itself, or the expensive path never goes away.
func TestLegacyNodeUpgradesItselfOnFirstUse(t *testing.T) {
	svc := authFixture(t)
	n, token, err := svc.CreateNode("tokyo", "jp.example.com", "JP")
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce the pre-migration state.
	if _, err := svc.Store().DB().Exec(
		`UPDATE nodes SET token_sha = NULL WHERE id = ?`, n.ID); err != nil {
		t.Fatal(err)
	}
	if missing, _ := svc.Store().NodesMissingTokenSHA(); missing != 1 {
		t.Fatalf("fixture did not take: %d nodes missing the index", missing)
	}

	slow := 0
	id, err := svc.AuthenticateNode(token, func() bool { slow++; return true })
	if err != nil || id != n.ID {
		t.Fatalf("a legacy token stopped working: %d %v", id, err)
	}
	if slow != 1 {
		t.Errorf("the slow path ran %d times, want exactly 1", slow)
	}

	// Second time it must be on the fast path.
	slow = 0
	if _, err := svc.AuthenticateNode(token, func() bool { slow++; return true }); err != nil {
		t.Fatal(err)
	}
	if slow != 0 {
		t.Error("the node did not upgrade itself; it is still taking the slow path")
	}
	if missing, _ := svc.Store().NodesMissingTokenSHA(); missing != 0 {
		t.Errorf("%d nodes still need the slow path", missing)
	}
}

// The throttle has to be consulted before the bcrypt, not after — a limiter
// that runs once the work is done throttles nothing.
func TestSlowPathIsRefusedWhenThrottled(t *testing.T) {
	svc := authFixture(t)
	n, token, err := svc.CreateNode("tokyo", "jp.example.com", "JP")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Store().DB().Exec(
		`UPDATE nodes SET token_sha = NULL WHERE id = ?`, n.ID); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, err = svc.AuthenticateNode(token, func() bool { return false })
	if !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("a throttled attempt returned %v, want ErrTooManyAttempts", err)
	}
	// If the bcrypt had run anyway this would be tens of milliseconds.
	if d := time.Since(start); d > 5*time.Millisecond {
		t.Errorf("a refused attempt still cost %v, so the work ran before the check", d)
	}
	// And it must not be reported as a bad token: a node that is merely being
	// throttled should back off, not conclude its credential is wrong.
	if errors.Is(err, ErrBadCredentials) {
		t.Error("throttling is indistinguishable from a bad token")
	}
}
