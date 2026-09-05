// Package singbox describes sing-box's configuration as Go structs.
//
// It deliberately contains nothing but type definitions and marshalling. In
// particular it must never import github.com/sagernet/sing-box: that package is
// GPL-3.0, and linking it would make this panel a derivative work and erase the
// licence boundary the two repositories are built around. The node is where
// sing-box actually runs; here we only describe what to send it.
//
// Only the subset the panel emits is modelled — three inbound protocols and the
// two API services the node needs for accounting. Anything else a node might
// support is out of scope by design.
package singbox

import "encoding/json"

type Config struct {
	Log          *Log          `json:"log,omitempty"`
	Inbounds     []Inbound     `json:"inbounds"`
	Outbounds    []Outbound    `json:"outbounds"`
	Route        *ServerRoute  `json:"route,omitempty"`
	Experimental *Experimental `json:"experimental,omitempty"`
}

// ServerRoute is what the node does with a connection once it has been
// authenticated. Absent means everything goes out, which is what a node with no
// policy configured gets.
//
// Named apart from the client's Route in client.go on purpose: they serialise
// to the same key in two different documents and mean opposite things. One
// picks which outbound a client should use; this one decides what a server
// refuses to carry.
type ServerRoute struct {
	Rules []ServerRouteRule `json:"rules,omitempty"`
}

// ServerRouteRule matches a connection and says what to do with it. The fields
// are a subset of sing-box's: the ones a policy expressed in the panel can
// produce.
//
// An empty matcher matches everything, which is how the sniffing rule is
// written — it has to run before any rule that matches on what was sniffed.
type ServerRouteRule struct {
	Protocol      []string `json:"protocol,omitempty"`
	Domain        []string `json:"domain,omitempty"`
	DomainSuffix  []string `json:"domain_suffix,omitempty"`
	DomainKeyword []string `json:"domain_keyword,omitempty"`
	Port          []int    `json:"port,omitempty"`
	Network       []string `json:"network,omitempty"`

	Action  string   `json:"action"`
	Sniffer []string `json:"sniffer,omitempty"`
	Timeout string   `json:"timeout,omitempty"`
}

type Log struct {
	Level     string `json:"level,omitempty"`
	Timestamp bool   `json:"timestamp,omitempty"`
}

type Inbound struct {
	Type       string `json:"type"`
	Tag        string `json:"tag"`
	Listen     string `json:"listen"`
	ListenPort int    `json:"listen_port"`

	// Users is empty in a stored inbound and in the config message. The node
	// merges the authoritative list from a separate `users` message, which is
	// what makes a user change a hot swap rather than a listener rebuild.
	Users []User `json:"users,omitempty"`

	TLS *TLS `json:"tls,omitempty"`

	// Shadowsocks only. Password here is the *server* half of the SS2022 key
	// pair; each user carries their own half.
	Method   string   `json:"method,omitempty"`
	Password string   `json:"password,omitempty"`
	Network  []string `json:"network,omitempty"`
}

// User covers all three protocols. Name is the billing identity the node
// reports traffic against and is the same string everywhere for a given user.
type User struct {
	Name string `json:"name"`

	UUID string `json:"uuid,omitempty"` // VLESS
	Flow string `json:"flow,omitempty"` // VLESS, xtls-rprx-vision

	Password string `json:"password,omitempty"` // AnyTLS, Shadowsocks
}

type TLS struct {
	Enabled    bool     `json:"enabled"`
	ServerName string   `json:"server_name,omitempty"`
	ALPN       []string `json:"alpn,omitempty"`

	// sing-box reads these files once, at start. Rotating a certificate
	// therefore requires restarting the listener, not just replacing the file.
	CertificatePath string `json:"certificate_path,omitempty"`
	KeyPath         string `json:"key_path,omitempty"`

	Reality *Reality `json:"reality,omitempty"`
}

type Reality struct {
	Enabled   bool      `json:"enabled"`
	Handshake Handshake `json:"handshake"`

	// PrivateKey is base64url *without* padding: sing-box decodes it with
	// base64.RawURLEncoding, and a trailing '=' fails outright.
	PrivateKey string   `json:"private_key"`
	ShortID    []string `json:"short_id"`
}

type Handshake struct {
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
}

type Outbound struct {
	Type string `json:"type"`
	Tag  string `json:"tag"`
}

type Experimental struct {
	ClashAPI *ClashAPI `json:"clash_api,omitempty"`
	V2RayAPI *V2RayAPI `json:"v2ray_api,omitempty"`
}

// ClashAPI backs online-user and connection queries on the node.
type ClashAPI struct {
	ExternalController string `json:"external_controller"`
}

// V2RayAPI backs per-user traffic counters.
type V2RayAPI struct {
	Listen string `json:"listen"`
	Stats  Stats  `json:"stats"`
}

type Stats struct {
	Enabled bool     `json:"enabled"`
	Users   []string `json:"users"`
}

func (c *Config) JSON() ([]byte, error) { return json.MarshalIndent(c, "", "  ") }
