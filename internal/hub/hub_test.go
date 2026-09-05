package hub

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kosje/skysb-panel/internal/service"
	"github.com/kosje/skysb-panel/internal/singbox"
	"github.com/kosje/skysb-panel/internal/store"
)

// fakeNode is the other half of the protocol: it dials the panel, says hello,
// and records what it is told. It exists so the control channel can be
// exercised end to end before the real node is written — and so the real node
// has an executable specification to match.
type fakeNode struct {
	t    *testing.T
	ws   *websocket.Conn
	recv chan Envelope
}

func dialNode(t *testing.T, srvURL, token string) *fakeNode {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srvURL, "http") + "/api/v1/node/connect"
	ws, _, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	n := &fakeNode{t: t, ws: ws, recv: make(chan Envelope, 32)}
	go n.readLoop()
	t.Cleanup(func() { ws.Close(websocket.StatusNormalClosure, "") })
	return n
}

func (n *fakeNode) readLoop() {
	for {
		_, data, err := n.ws.Read(context.Background())
		if err != nil {
			close(n.recv)
			return
		}
		var env Envelope
		if json.Unmarshal(data, &env) == nil {
			n.recv <- env
		}
	}
}

func (n *fakeNode) send(t string, id uint64, data any) {
	n.t.Helper()
	frame, err := encode(t, id, data)
	if err != nil {
		n.t.Fatalf("encode %s: %v", t, err)
	}
	if err := n.ws.Write(context.Background(), websocket.MessageText, frame); err != nil {
		n.t.Fatalf("write %s: %v", t, err)
	}
}

// await waits for the next frame of the given type, ignoring others (a ping can
// arrive at any moment and must not fail an unrelated assertion).
func (n *fakeNode) await(msgType string, within time.Duration) Envelope {
	n.t.Helper()
	deadline := time.After(within)
	for {
		select {
		case env, ok := <-n.recv:
			if !ok {
				n.t.Fatalf("connection closed while waiting for %q", msgType)
			}
			if env.Type == msgType {
				return env
			}
		case <-deadline:
			n.t.Fatalf("timed out waiting for %q", msgType)
		}
	}
}

// ── harness ─────────────────────────────────────────────────────────────────

type harness struct {
	svc   *service.Service
	hub   *Hub
	srv   *httptest.Server
	node  *store.Node
	token string
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	svc := service.New(st)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := New(svc, log)
	svc.SetNotifier(h)

	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/node/connect", h.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	node, token, err := svc.CreateNode("tokyo", "jp.example.com", "JP")
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	return &harness{svc: svc, hub: h, srv: srv, node: node, token: token}
}

func (h *harness) addInbounds(t *testing.T) {
	t.Helper()
	specs := []service.InboundSpec{
		{Protocol: store.ProtoVLESS, Port: 443, Handshake: service.DefaultHandshake},
		{Protocol: store.ProtoAnyTLS, Port: 8443, CertPath: "/c.pem", KeyPath: "/k.pem",
			ServerName: "jp.example.com"},
		{Protocol: store.ProtoShadowsocks, Port: 8388},
	}
	for _, s := range specs {
		if _, err := h.svc.CreateInbound(h.node.ID, s); err != nil {
			t.Fatalf("create inbound %s: %v", s.Protocol, err)
		}
	}
}

// ── tests ───────────────────────────────────────────────────────────────────

func TestRejectsBadToken(t *testing.T) {
	h := newHarness(t)
	url := "ws" + strings.TrimPrefix(h.srv.URL, "http") + "/api/v1/node/connect"

	for _, tok := range []string{"", "not-a-token", h.token + "x"} {
		hdr := http.Header{}
		if tok != "" {
			hdr.Set("Authorization", "Bearer "+tok)
		}
		ws, _, err := websocket.Dial(context.Background(), url,
			&websocket.DialOptions{HTTPHeader: hdr})
		if err == nil {
			ws.Close(websocket.StatusNormalClosure, "")
			t.Fatalf("token %q was accepted", tok)
		}
	}
}

// A node that connects gets the current state without anyone editing anything:
// it keeps no state across a reconnect, so waiting for the next change would
// leave it serving nothing.
func TestHelloTriggersFullPush(t *testing.T) {
	h := newHarness(t)
	h.addInbounds(t)

	n := dialNode(t, h.srv.URL, h.token)
	n.send(TypeHello, 1, Hello{Version: "0.1.0", SingboxVersion: "1.14.0", Hostname: "n1"})

	cfgEnv := n.await(TypeConfig, 3*time.Second)
	var cfg ConfigData
	if err := json.Unmarshal(cfgEnv.Data, &cfg); err != nil {
		t.Fatalf("config payload: %v", err)
	}
	if got := len(cfg.Config.Inbounds); got != 3 {
		t.Fatalf("config has %d inbounds, want 3", got)
	}

	usersEnv := n.await(TypeUsers, 3*time.Second)
	var users UsersData
	if err := json.Unmarshal(usersEnv.Data, &users); err != nil {
		t.Fatalf("users payload: %v", err)
	}
	if len(users.ByTag) != 3 {
		t.Fatalf("users keyed by %d tags, want 3", len(users.ByTag))
	}
}

// The config carries no users at all. That separation is what makes a user
// change a hot swap instead of a listener rebuild, and a rebuild drops every
// live connection on the inbound.
func TestConfigCarriesNoUsers(t *testing.T) {
	h := newHarness(t)
	h.addInbounds(t)
	if _, err := h.svc.CreateUser(service.NewUser{Name: "alice"}); err != nil {
		t.Fatalf("create user: %v", err)
	}

	n := dialNode(t, h.srv.URL, h.token)
	n.send(TypeHello, 1, Hello{Version: "0.1.0"})

	var cfg ConfigData
	if err := json.Unmarshal(n.await(TypeConfig, 3*time.Second).Data, &cfg); err != nil {
		t.Fatalf("config payload: %v", err)
	}
	for _, in := range cfg.Config.Inbounds {
		if len(in.Users) != 0 {
			t.Errorf("inbound %s carries %d users in the config message", in.Tag, len(in.Users))
		}
	}

	// The v2ray_api allowlist must also be empty in the config: it is built
	// when the config loads, so anything sent here is stale by the time the
	// first user changes — and a user missing from it is never billed.
	if got := cfg.Config.Experimental.V2RayAPI.Stats.Users; len(got) != 0 {
		t.Errorf("config pre-seeds the stats allowlist with %v", got)
	}
}

// Each protocol must receive the credential it actually authenticates with, and
// only that one: shipping all three would put two extra secrets per user into
// every inbound for no benefit.
func TestUsersCarryTheRightCredential(t *testing.T) {
	h := newHarness(t)
	h.addInbounds(t)
	if _, err := h.svc.CreateUser(service.NewUser{Name: "alice"}); err != nil {
		t.Fatalf("create user: %v", err)
	}

	n := dialNode(t, h.srv.URL, h.token)
	n.send(TypeHello, 1, Hello{Version: "0.1.0"})

	var users UsersData
	if err := json.Unmarshal(n.await(TypeUsers, 3*time.Second).Data, &users); err != nil {
		t.Fatalf("users payload: %v", err)
	}

	check := func(tag string, want func(singbox.User) bool, desc string) {
		t.Helper()
		list, ok := users.ByTag[tag]
		if !ok || len(list) != 1 {
			t.Fatalf("tag %s: got %d users, want 1", tag, len(list))
		}
		if !want(list[0]) {
			t.Errorf("tag %s: %s (got %+v)", tag, desc, list[0])
		}
	}
	check("vless-tokyo", func(u singbox.User) bool {
		return u.UUID != "" && u.Flow == service.FlowVision && u.Password == ""
	}, "expected a uuid and vision flow, and no password")
	check("anytls-tokyo", func(u singbox.User) bool {
		return u.Password != "" && u.UUID == ""
	}, "expected a password and no uuid")
	check("ss-tokyo", func(u singbox.User) bool {
		return u.Password != "" && u.UUID == ""
	}, "expected a password and no uuid")

	// StatsUsers is what the node installs as the billing allowlist. A name
	// missing from it relays traffic that is never counted.
	if len(users.StatsUsers) != 1 || users.StatsUsers[0] != "alice" {
		t.Errorf("stats allowlist = %v, want [alice]", users.StatsUsers)
	}
}

// Adding a user must reach a connected node without anything else happening.
func TestUserChangePushesToConnectedNode(t *testing.T) {
	h := newHarness(t)
	h.addInbounds(t)

	n := dialNode(t, h.srv.URL, h.token)
	n.send(TypeHello, 1, Hello{Version: "0.1.0"})
	n.await(TypeUsers, 3*time.Second) // the initial push

	if _, err := h.svc.CreateUser(service.NewUser{Name: "bob"}); err != nil {
		t.Fatalf("create user: %v", err)
	}

	var users UsersData
	if err := json.Unmarshal(n.await(TypeUsers, 3*time.Second).Data, &users); err != nil {
		t.Fatalf("users payload: %v", err)
	}
	if len(users.ByTag["vless-tokyo"]) != 1 {
		t.Fatalf("expected bob to be pushed, got %+v", users.ByTag["vless-tokyo"])
	}
}

// Access is revoked by omission rather than by a disable command, so a disabled
// user simply stops appearing.
func TestDisabledUserIsOmitted(t *testing.T) {
	h := newHarness(t)
	h.addInbounds(t)
	u, err := h.svc.CreateUser(service.NewUser{Name: "carol"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	n := dialNode(t, h.srv.URL, h.token)
	n.send(TypeHello, 1, Hello{Version: "0.1.0"})
	n.await(TypeUsers, 3*time.Second)

	u.Enabled = false
	if err := h.svc.UpdateUser(u); err != nil {
		t.Fatalf("disable: %v", err)
	}

	var users UsersData
	if err := json.Unmarshal(n.await(TypeUsers, 3*time.Second).Data, &users); err != nil {
		t.Fatalf("users payload: %v", err)
	}
	for tag, list := range users.ByTag {
		if len(list) != 0 {
			t.Errorf("tag %s still carries %d users after disable", tag, len(list))
		}
	}
	if len(users.StatsUsers) != 0 {
		t.Errorf("stats allowlist still has %v", users.StatsUsers)
	}
}

// Traffic arrives as deltas and accumulates. Reporting the same delta twice has
// to double the total — that is what makes a node restart, which zeroes its own
// counters, harmless.
func TestStatsAccumulate(t *testing.T) {
	h := newHarness(t)
	h.addInbounds(t)
	u, err := h.svc.CreateUser(service.NewUser{Name: "dave"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	n := dialNode(t, h.srv.URL, h.token)
	n.send(TypeHello, 1, Hello{Version: "0.1.0"})
	n.await(TypeUsers, 3*time.Second)

	for i := 0; i < 2; i++ {
		n.send(TypeStats, 0, StatsData{Users: map[string]Usage{
			"dave": {Up: 1000, Down: 9000},
		}})
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		got, err := h.svc.User(u.ID)
		if err != nil {
			t.Fatalf("read user: %v", err)
		}
		if got.TrafficUsed == 20000 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("traffic_used = %d, want 20000", got.TrafficUsed)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Crossing the limit has to revoke access on its own, without an operator
// noticing and disabling the user by hand.
func TestTrafficLimitRevokesAccess(t *testing.T) {
	h := newHarness(t)
	h.addInbounds(t)
	if _, err := h.svc.CreateUser(service.NewUser{Name: "erin", TrafficLimit: 10000}); err != nil {
		t.Fatalf("create user: %v", err)
	}

	n := dialNode(t, h.srv.URL, h.token)
	n.send(TypeHello, 1, Hello{Version: "0.1.0"})
	n.await(TypeUsers, 3*time.Second)

	n.send(TypeStats, 0, StatsData{Users: map[string]Usage{
		"erin": {Up: 5000, Down: 6000}, // 11000 > 10000
	}})

	// The traffic report itself triggers the push that drops her.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("user was never dropped after exceeding the limit")
		default:
		}
		env := n.await(TypeUsers, 3*time.Second)
		var users UsersData
		if err := json.Unmarshal(env.Data, &users); err != nil {
			t.Fatalf("users payload: %v", err)
		}
		if len(users.StatsUsers) == 0 {
			return
		}
	}
}

// Traffic for a name the panel does not know must be dropped rather than
// inserted: a node can still be running a user list from before a deletion.
func TestUnknownUserTrafficIsIgnored(t *testing.T) {
	h := newHarness(t)
	h.addInbounds(t)

	n := dialNode(t, h.srv.URL, h.token)
	n.send(TypeHello, 1, Hello{Version: "0.1.0"})
	n.await(TypeUsers, 3*time.Second)

	n.send(TypeStats, 0, StatsData{Users: map[string]Usage{
		"ghost": {Up: 1 << 20, Down: 1 << 20},
	}})
	// Nothing to assert beyond survival: the connection must stay up and the
	// panel must not have written a row for a user that does not exist.
	n.send(TypeHello, 2, Hello{Version: "0.1.0"})
	n.await(TypeConfig, 3*time.Second)
}

func TestConnectedTracksLifecycle(t *testing.T) {
	h := newHarness(t)

	if h.hub.Connected(h.node.ID) {
		t.Fatal("node reported connected before dialling")
	}
	n := dialNode(t, h.srv.URL, h.token)
	n.send(TypeHello, 1, Hello{Version: "0.1.0"})
	n.await(TypeConfig, 3*time.Second)

	if !h.hub.Connected(h.node.ID) {
		t.Fatal("node not reported connected after hello")
	}

	n.ws.Close(websocket.StatusNormalClosure, "bye")
	deadline := time.Now().Add(3 * time.Second)
	for h.hub.Connected(h.node.ID) {
		if time.Now().After(deadline) {
			t.Fatal("node still reported connected after close")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A reconnect after a network drop arrives before the old side has noticed. The
// newest connection has to win, or pushes would go to a socket nobody reads.
func TestReconnectReplacesOldConnection(t *testing.T) {
	h := newHarness(t)
	h.addInbounds(t)

	first := dialNode(t, h.srv.URL, h.token)
	first.send(TypeHello, 1, Hello{Version: "0.1.0"})
	first.await(TypeConfig, 3*time.Second)

	second := dialNode(t, h.srv.URL, h.token)
	second.send(TypeHello, 1, Hello{Version: "0.1.0"})
	second.await(TypeConfig, 3*time.Second)

	// The first connection must have been closed by the hub.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-first.recv:
			if !ok {
				return // closed, as required
			}
		case <-deadline:
			t.Fatal("the superseded connection was left open")
		}
	}
}

func TestOnlineUsers(t *testing.T) {
	h := newHarness(t)
	h.addInbounds(t)

	n := dialNode(t, h.srv.URL, h.token)
	n.send(TypeHello, 1, Hello{Version: "0.1.0"})
	n.await(TypeUsers, 3*time.Second)

	n.send(TypeOnline, 0, OnlineData{Users: []string{"alice", "bob"}})

	deadline := time.Now().Add(3 * time.Second)
	for {
		online := h.hub.OnlineUsers()
		if online["alice"] && online["bob"] {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("online users = %v, want alice and bob", online)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
