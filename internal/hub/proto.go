// Package hub owns the node control channel: one WebSocket per node, dialled by
// the node rather than by the panel.
//
// That direction is the whole design. A node needs no inbound control port, no
// certificate of its own for the control plane, and no route from the panel —
// so it works behind NAT, and the panel never has to know how to reach it. The
// node's own address matters only to clients, and only for subscriptions.
package hub

import (
	"encoding/json"

	"github.com/kosje/skysbx-panel/internal/singbox"
)

// Message types. Three commands travel down; the rest are the node reporting.
const (
	// node -> panel
	TypeHello  = "hello"
	TypeOK     = "ok"
	TypeError  = "error"
	TypeStats  = "stats"
	TypeOnline = "online"
	TypeState  = "state"
	TypePong   = "pong"

	// panel -> node
	TypeConfig = "config"
	TypeUsers  = "users"
	TypePing   = "ping"
)

// Envelope wraps every frame. ID correlates a command with its reply and is
// zero for one-way reports.
type Envelope struct {
	Type string          `json:"t"`
	ID   uint64          `json:"id,omitempty"`
	Data json.RawMessage `json:"d,omitempty"`
}

type Hello struct {
	Version        string `json:"version"`
	OS             string `json:"os"`
	Arch           string `json:"arch"`
	Hostname       string `json:"hostname"`
	SingboxVersion string `json:"singbox_version"`
}

// ErrorData explains a rejected command. A node that cannot apply a config —
// a certificate path that does not exist, a port already bound — has to say so
// rather than fail silently, or the panel will keep showing a configuration the
// node never adopted.
type ErrorData struct {
	Message string `json:"msg"`
}

// ConfigData carries the sing-box configuration. Inbound user lists are empty;
// see UsersData.
type ConfigData struct {
	Config *singbox.Config `json:"config"`
}

// UsersData is the authoritative user list keyed by inbound tag. It replaces
// whatever the node has — it is not a delta — so a node that missed a message
// converges as soon as it receives the next one.
//
// StatsUsers is the flat list of names for the node's v2ray_api allowlist.
// Sending it alongside is deliberate: that allowlist is built when a config
// loads, so a hot-added user missing from it relays traffic that is never
// counted, with nothing in any log to say so.
type UsersData struct {
	ByTag      map[string][]singbox.User `json:"by_tag"`
	StatsUsers []string                  `json:"stats_users"`
}

// StatsData reports traffic since the previous report, not cumulative totals.
// A node restart zeroes its own counters; deltas make that a gap rather than a
// total that jumps backwards.
type StatsData struct {
	Users  map[string]Usage `json:"users"`
	System *SystemStats     `json:"system,omitempty"`
}

type Usage struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

type SystemStats struct {
	CPU      float64 `json:"cpu"`
	MemUsed  int64   `json:"mem_used"`
	MemTotal int64   `json:"mem_total"`
	Uptime   int64   `json:"uptime"`
}

// OnlineData lists users with at least one live connection right now.
type OnlineData struct {
	Users []string `json:"users"`
}

// StateData is the node's own account of what it is serving, sent after every
// attempt to apply a configuration.
//
// An error reply says the node refused a configuration; this says what it is
// running instead. Both are needed. A node that rejects a configuration keeps
// serving the previous one, so without this the panel would show an inbound as
// enabled — the operator asked for it, after all — while the node has never
// heard of it, and the only symptom would be users unable to connect to that
// one port.
type StateData struct {
	Inbounds []string `json:"inbounds"`
	Error    string   `json:"error,omitempty"`
}

func encode(t string, id uint64, data any) ([]byte, error) {
	env := Envelope{Type: t, ID: id}
	if data != nil {
		raw, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		env.Data = raw
	}
	return json.Marshal(env)
}
