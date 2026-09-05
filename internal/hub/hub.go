package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/kosje/skysbx-panel/internal/service"
)

const (
	pingEvery   = 30 * time.Second
	pongTimeout = 90 * time.Second

	// A config for a node with many inbounds is still only a few KB; anything
	// larger is a bug or an attempt to exhaust memory.
	maxFrame = 1 << 20

	// Coalescing window for pushes. A bulk edit in the UI fires one
	// notification per row; without this each one would be its own frame.
	coalesce = 100 * time.Millisecond
)

// Hub tracks the nodes currently connected and pushes to them.
type Hub struct {
	svc *service.Service
	log *slog.Logger

	mu    sync.RWMutex
	conns map[int64]*conn // by node id

	nextID atomic.Uint64

	// Pending pushes, collapsed by the coalescing timer.
	pendMu     sync.Mutex
	pendUsers  bool
	pendConfig map[int64]bool
	pendTimer  *time.Timer
}

func New(svc *service.Service, log *slog.Logger) *Hub {
	return &Hub{
		svc:        svc,
		log:        log,
		conns:      map[int64]*conn{},
		pendConfig: map[int64]bool{},
	}
}

type conn struct {
	nodeID int64
	ws     *websocket.Conn

	writeMu sync.Mutex // a websocket allows one writer at a time

	lastSeen atomic.Int64 // unix seconds, for pong tracking
	online   atomic.Pointer[[]string]
	ips      atomic.Pointer[map[string]int]
	state    atomic.Pointer[StateData]
}

// ── connection lifecycle ────────────────────────────────────────────────────

// Handler serves the node control channel. Nodes authenticate with a bearer
// token; there is no certificate exchange, because the node dials the panel and
// the panel's own TLS already authenticates the endpoint the node reached.
func (h *Hub) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r)
		if token == "" {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		nodeID, err := h.svc.AuthenticateNode(token)
		if err != nil {
			h.log.Warn("node authentication failed", "remote", r.RemoteAddr)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			// Nodes are not browsers; there is no Origin to police, and
			// rejecting on it would only break non-browser clients.
			InsecureSkipVerify: true,
		})
		if err != nil {
			h.log.Warn("websocket accept", "node", nodeID, "error", err)
			return
		}
		ws.SetReadLimit(maxFrame)

		h.serve(r.Context(), nodeID, ws)
	}
}

func bearer(r *http.Request) string {
	const prefix = "Bearer "
	v := r.Header.Get("Authorization")
	if len(v) > len(prefix) && strings.EqualFold(v[:len(prefix)], prefix) {
		return strings.TrimSpace(v[len(prefix):])
	}
	return ""
}

func (h *Hub) serve(ctx context.Context, nodeID int64, ws *websocket.Conn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	c := &conn{nodeID: nodeID, ws: ws}
	c.lastSeen.Store(time.Now().Unix())

	// One connection per node. A reconnect after a network drop arrives before
	// the old side has noticed, so the previous connection is closed rather
	// than left to shadow the new one.
	h.mu.Lock()
	if old, ok := h.conns[nodeID]; ok {
		old.ws.Close(websocket.StatusNormalClosure, "replaced by a new connection")
	}
	h.conns[nodeID] = c
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		if h.conns[nodeID] == c {
			delete(h.conns, nodeID)
		}
		h.mu.Unlock()
		ws.Close(websocket.StatusNormalClosure, "")
		h.log.Info("node disconnected", "node", nodeID)
	}()

	go h.keepalive(ctx, c)

	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			if !errors.Is(err, context.Canceled) &&
				websocket.CloseStatus(err) == -1 {
				h.log.Debug("node read", "node", nodeID, "error", err)
			}
			return
		}
		c.lastSeen.Store(time.Now().Unix())
		if err := h.dispatch(ctx, c, data); err != nil {
			h.log.Warn("node message", "node", nodeID, "error", err)
		}
	}
}

func (h *Hub) keepalive(ctx context.Context, c *conn) {
	t := time.NewTicker(pingEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// A TCP connection through a dead middlebox stays writable long
			// after it stops delivering. Silence, not write failure, is what
			// identifies a node that is gone.
			if time.Since(time.Unix(c.lastSeen.Load(), 0)) > pongTimeout {
				h.log.Warn("node stopped responding", "node", c.nodeID)
				c.ws.Close(websocket.StatusGoingAway, "no response")
				return
			}
			if err := h.send(ctx, c, TypePing, h.nextID.Add(1), nil); err != nil {
				return
			}
		}
	}
}

// ── inbound messages ────────────────────────────────────────────────────────

func (h *Hub) dispatch(ctx context.Context, c *conn, data []byte) error {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("malformed envelope: %w", err)
	}

	switch env.Type {
	case TypeHello:
		var hello Hello
		if len(env.Data) > 0 {
			if err := json.Unmarshal(env.Data, &hello); err != nil {
				return fmt.Errorf("hello: %w", err)
			}
		}
		version := strings.TrimSpace(hello.Version + " / sing-box " + hello.SingboxVersion)
		if err := h.svc.MarkNodeSeen(c.nodeID, version); err != nil {
			h.log.Warn("mark node seen", "node", c.nodeID, "error", err)
		}
		h.log.Info("node connected", "node", c.nodeID,
			"version", hello.Version, "singbox", hello.SingboxVersion,
			"host", hello.Hostname)
		// A node has no state of its own worth keeping across a reconnect, so
		// it is brought up to date immediately rather than waiting for the next
		// edit in the UI.
		return h.pushTo(ctx, c, true, true)

	case TypeOK:
		return nil

	case TypeError:
		var e ErrorData
		_ = json.Unmarshal(env.Data, &e)
		// Logged at error level on purpose: this is the node telling us it did
		// not adopt what we sent, which is otherwise invisible from the panel.
		h.log.Error("node rejected a command", "node", c.nodeID, "id", env.ID, "msg", e.Message)
		return nil

	case TypeStats:
		var s StatsData
		if err := json.Unmarshal(env.Data, &s); err != nil {
			return fmt.Errorf("stats: %w", err)
		}
		return h.applyStats(c.nodeID, s)

	case TypeOnline:
		var o OnlineData
		if err := json.Unmarshal(env.Data, &o); err != nil {
			return fmt.Errorf("online: %w", err)
		}
		names := append([]string(nil), o.Users...)
		c.online.Store(&names)
		ips := make(map[string]int, len(o.IPs))
		for name, n := range o.IPs {
			ips[name] = n
		}
		c.ips.Store(&ips)
		return nil

	case TypeState:
		var st StateData
		if err := json.Unmarshal(env.Data, &st); err != nil {
			return fmt.Errorf("state: %w", err)
		}
		c.state.Store(&st)
		if st.Error != "" {
			h.log.Warn("node is not serving what it was sent",
				"node", c.nodeID, "serving", st.Inbounds, "error", st.Error)
		}
		return nil

	case TypePong:
		return nil

	default:
		return fmt.Errorf("unknown message type %q", env.Type)
	}
}

func (h *Hub) applyStats(nodeID int64, s StatsData) error {
	if len(s.Users) == 0 {
		return nil
	}
	usage := make(map[string]service.Usage, len(s.Users))
	for name, u := range s.Users {
		if u.Up < 0 || u.Down < 0 {
			// Deltas are unsigned by construction. A negative one means the
			// node computed it wrong; counting it would corrupt a total that
			// nothing else can correct.
			h.log.Warn("negative traffic delta ignored",
				"node", nodeID, "user", name, "up", u.Up, "down", u.Down)
			continue
		}
		usage[name] = service.Usage{Up: u.Up, Down: u.Down}
	}
	return h.svc.RecordTraffic(nodeID, usage)
}

// ── outbound pushes ─────────────────────────────────────────────────────────

func (h *Hub) send(ctx context.Context, c *conn, t string, id uint64, data any) error {
	frame, err := encode(t, id, data)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.ws.Write(writeCtx, websocket.MessageText, frame)
}

// pushTo sends the current config and/or user list to one node.
func (h *Hub) pushTo(ctx context.Context, c *conn, config, users bool) error {
	if config {
		cfg, err := h.svc.NodeConfig(c.nodeID)
		if err != nil {
			return fmt.Errorf("build config for node %d: %w", c.nodeID, err)
		}
		if err := h.send(ctx, c, TypeConfig, h.nextID.Add(1),
			ConfigData{Config: cfg}); err != nil {
			return err
		}
	}
	if users {
		byTag, err := h.svc.NodeUsers(c.nodeID)
		if err != nil {
			return fmt.Errorf("build users for node %d: %w", c.nodeID, err)
		}
		limits, err := h.svc.UserIPLimits()
		if err != nil {
			return fmt.Errorf("read address limits: %w", err)
		}
		if err := h.send(ctx, c, TypeUsers, h.nextID.Add(1), UsersData{
			ByTag:      byTag,
			StatsUsers: service.StatsUsers(byTag),
			IPLimits:   limits,
		}); err != nil {
			return err
		}
	}
	return nil
}

// ── service.Notifier ────────────────────────────────────────────────────────

// UsersChanged is called by the service whenever the set of active users could
// have changed. Every connected node is refreshed: user membership is not
// per-node state the service tracks, and recomputing is cheap.
func (h *Hub) UsersChanged() { h.schedule(0, true) }

// ConfigChanged is called when a node's inbounds changed. Its users are pushed
// too, because a new inbound starts with an empty user list.
func (h *Hub) ConfigChanged(nodeID int64) { h.schedule(nodeID, true) }

func (h *Hub) schedule(nodeID int64, users bool) {
	h.pendMu.Lock()
	defer h.pendMu.Unlock()

	if users {
		h.pendUsers = true
	}
	if nodeID != 0 {
		h.pendConfig[nodeID] = true
	}
	if h.pendTimer == nil {
		h.pendTimer = time.AfterFunc(coalesce, h.flush)
	} else {
		h.pendTimer.Reset(coalesce)
	}
}

func (h *Hub) flush() {
	h.pendMu.Lock()
	users := h.pendUsers
	configs := h.pendConfig
	h.pendUsers = false
	h.pendConfig = map[int64]bool{}
	h.pendTimer = nil
	h.pendMu.Unlock()

	h.mu.RLock()
	conns := make([]*conn, 0, len(h.conns))
	for _, c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, c := range conns {
		wantConfig := configs[c.nodeID]
		if !wantConfig && !users {
			continue
		}
		if err := h.pushTo(ctx, c, wantConfig, users); err != nil {
			h.log.Warn("push to node", "node", c.nodeID, "error", err)
		}
	}
}

// ── status for the UI ───────────────────────────────────────────────────────

// Connected reports whether a node currently holds a control channel.
func (h *Hub) Connected(nodeID int64) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.conns[nodeID]
	return ok
}

// OnlineUsers returns the union of users reported online by every node.
func (h *Hub) OnlineUsers() map[string]bool {
	h.mu.RLock()
	conns := make([]*conn, 0, len(h.conns))
	for _, c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.RUnlock()

	out := map[string]bool{}
	for _, c := range conns {
		if names := c.online.Load(); names != nil {
			for _, n := range *names {
				out[n] = true
			}
		}
	}
	return out
}

// UserIPCounts is how many distinct source addresses each user is connected
// from, summed across nodes.
//
// Summed, not maxed: someone sharing an account across three nodes is using
// three nodes' worth of bandwidth, and the number an operator wants to see is
// how many places it is being used from. The cap itself is enforced per node —
// that is the only place a connection can be refused as it is made — so this
// figure can legitimately exceed the limit on a multi-node panel.
func (h *Hub) UserIPCounts() map[string]int {
	h.mu.RLock()
	conns := make([]*conn, 0, len(h.conns))
	for _, c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.RUnlock()

	out := map[string]int{}
	for _, c := range conns {
		if m := c.ips.Load(); m != nil {
			for name, n := range *m {
				out[name] += n
			}
		}
	}
	return out
}

// LiveInbounds is the set of inbound tags a node says it is serving right now.
//
// known is false for a node that is disconnected or has not reported yet, which
// is not the same as a node serving nothing: the panel says nothing at all in
// that case rather than marking every inbound as down.
//
// Returned as primitives so the web package can state what it needs without
// importing this one.
func (h *Hub) LiveInbounds(nodeID int64) (tags map[string]bool, known bool) {
	st := h.stateOf(nodeID)
	if st == nil {
		return nil, false
	}
	tags = make(map[string]bool, len(st.Inbounds))
	for _, t := range st.Inbounds {
		tags[t] = true
	}
	return tags, true
}

// ApplyError is why a node is not serving what it was last sent, or empty if it
// is — or if it has not said.
func (h *Hub) ApplyError(nodeID int64) string {
	if st := h.stateOf(nodeID); st != nil {
		return st.Error
	}
	return ""
}

func (h *Hub) stateOf(nodeID int64) *StateData {
	h.mu.RLock()
	c := h.conns[nodeID]
	h.mu.RUnlock()
	if c == nil {
		return nil
	}
	return c.state.Load()
}

// Compile-time proof that the hub satisfies what the service expects. Without
// it a signature drift would leave the service silently using nopNotifier and
// nothing would ever reach a node.
var _ service.Notifier = (*Hub)(nil)
