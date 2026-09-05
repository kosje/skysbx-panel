package store

import "time"

// Protocol values stored in inbounds.protocol. They match sing-box's own
// inbound type names, because the config column is sing-box JSON.
const (
	ProtoVLESS       = "vless"
	ProtoAnyTLS      = "anytls"
	ProtoShadowsocks = "shadowsocks"
)

type User struct {
	ID   int64
	Name string

	// One credential per protocol. All three are handed to every inbound the
	// user is allowed on; which one matters depends on the protocol.
	VlessUUID  string // VLESS
	Password   string // AnyTLS, plain
	SSPassword string // Shadowsocks 2022 user PSK, base64 of 32 bytes

	SubToken     string
	Enabled      bool
	ExpiresAt    *time.Time // nil = never expires
	TrafficLimit int64      // bytes, 0 = unlimited
	TrafficUsed  int64
	Note         string
	CreatedAt    time.Time
}

// Active reports whether the user should currently be present in the user list
// pushed to nodes. Everything that can revoke access is decided here, in one
// place, so the hub never has to reimplement it.
func (u *User) Active(now time.Time) bool {
	if !u.Enabled {
		return false
	}
	if u.ExpiresAt != nil && now.After(*u.ExpiresAt) {
		return false
	}
	if u.TrafficLimit > 0 && u.TrafficUsed >= u.TrafficLimit {
		return false
	}
	return true
}

type Node struct {
	ID        int64
	Name      string
	TokenHash string
	// Address is what clients connect to — a domain, or a bare IP for Reality
	// and Shadowsocks. It is not how the panel reaches the node: nodes dial the
	// panel, so the panel never needs a route to them.
	Address    string
	Country    string
	Enabled    bool
	LastSeenAt *time.Time
	Version    string
	CreatedAt  time.Time
}

type Inbound struct {
	ID       int64
	NodeID   int64
	Tag      string
	Protocol string
	Port     int

	// Config is the sing-box inbound object, with an empty users array. Users
	// are pushed separately so they can be hot-swapped without rebuilding the
	// listener.
	Config string
	// Client holds the parameters a client needs but the server does not:
	// Reality's public key and short id, the SNI, the flow. Derived once when
	// the inbound is created rather than recomputed per subscription.
	Client string

	Enabled bool
}
