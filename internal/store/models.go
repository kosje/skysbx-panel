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

	// IPLimit caps how many distinct source addresses this user may have
	// connected at once, per node. Zero is no limit.
	//
	// It exists because a subscription is a file: nothing stops the person it
	// was issued to from posting it, and without a cap one account becomes
	// fifty on the same bandwidth bill. Enforced on the node, which is the only
	// place that sees a connection as it is made.
	IPLimit int

	Note      string
	CreatedAt time.Time
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
	ID   int64
	Name string

	// TokenHash is bcrypt of the join token, kept for nodes created before
	// TokenSHA existed. TokenSHA is SHA-256 of the same token, hex, and is what
	// authentication actually looks up — a join token is 32 random bytes, so
	// there is nothing for bcrypt's slowness to defend against, and a linear
	// scan of bcrypt hashes is a denial of service waiting to be found.
	//
	// TokenSHA is empty only for a node whose token was minted before the
	// column existed; it fills itself in on that node's next handshake.
	TokenHash string
	TokenSHA  string
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

	// Address is where clients should be told to connect for this inbound, when
	// that is not the node's own address. The case it exists for is a relay:
	// another host forwards a port straight through, so clients dial the relay
	// and the node never appears in a subscription. That is a property of one
	// inbound — the same node can have one port relayed and the rest direct.
	//
	// Empty means the node's address, which is almost every inbound.
	//
	// This is the *external* form: some host the panel knows nothing about,
	// running realm or nginx stream. RelayNodeID below is the same idea done by
	// a node this panel already manages, and the two are mutually exclusive.
	Address string

	// RelayNodeID is another node that carries this inbound's traffic: it runs a
	// `direct` listener on RelayPort and copies bytes to this node's port. Zero
	// means clients reach this inbound directly.
	//
	// Nothing is decrypted on the way through, so the origin node still
	// terminates the protocol and still sees every user individually. That is
	// the whole point of relaying at layer 4 instead of chaining proxies.
	RelayNodeID int64
	RelayPort   int

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
