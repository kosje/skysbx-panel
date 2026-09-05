package service

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/kosje/skysbx-panel/internal/singbox"
	"github.com/kosje/skysbx-panel/internal/store"
)

// InboundSpec is what the UI collects. Everything else — keys, short ids,
// server PSKs — is derived, because there is no reason to make a human type a
// 32-byte key correctly.
type InboundSpec struct {
	Protocol string
	Tag      string
	Port     int

	// VLESS+Reality: the site whose TLS handshake is borrowed. "host" or
	// "host:port"; the port defaults to 443. It must speak TLS 1.3 and HTTP/2.
	Handshake string

	// AnyTLS: paths *on the node* to a certificate for the node's own domain.
	// Reality and Shadowsocks need neither, which is why a node without a
	// certificate still serves two of the three protocols.
	CertPath string
	KeyPath  string
	// ServerName goes in the TLS handshake; normally the node's own domain.
	ServerName string

	// Address overrides what subscriptions tell clients to connect to for this
	// inbound. Blank means the node's own address. It exists for relays: a
	// separate host forwarding this port through to the node, which clients
	// dial instead.
	Address string
}

// ClientParams are the bits a client needs and the server does not: Reality's
// public key and short id, the SNI to present, the flow to use. They are
// derived once here rather than recomputed on every subscription fetch — the
// public key in particular is a scalar multiplication we would otherwise repeat
// for every user, on every request.
type ClientParams struct {
	SNI  string `json:"sni,omitempty"`
	PBK  string `json:"pbk,omitempty"` // Reality public key, base64url unpadded
	SID  string `json:"sid,omitempty"` // Reality short id
	FP   string `json:"fp,omitempty"`  // uTLS fingerprint
	Flow string `json:"flow,omitempty"`

	Method    string `json:"method,omitempty"`     // Shadowsocks
	ServerPSK string `json:"server_psk,omitempty"` // Shadowsocks, server half of the key pair
}

// SSMethod is the only Shadowsocks method this panel emits. The user PSK it
// generates is 32 bytes, which is exactly an aes-256 key; pairing that with a
// 128-bit method would be a runtime key-length failure on the node.
const SSMethod = "2022-blake3-aes-256-gcm"

// DefaultHandshake is a site that reliably speaks TLS 1.3 + H2.
const DefaultHandshake = "www.microsoft.com:443"

// Where install-node.sh writes the certificate it obtains, and where its certbot
// deploy hook copies each renewal to. AnyTLS inbounds default to these so the
// common case needs no typing; a node keeping its certificate elsewhere can
// still say where.
const (
	DefaultCertPath = "/opt/skysbx/cert.pem"
	DefaultKeyPath  = "/opt/skysbx/key.pem"
)

// FlowVision is the only VLESS flow this panel emits. It has to be identical in
// the inbound's client parameters and in every user pushed to that inbound: a
// mismatch is rejected at handshake time as "flow mismatch", which reads like a
// client problem rather than a panel one.
const FlowVision = "xtls-rprx-vision"

// BuildInbound turns a spec into a stored inbound: the sing-box config to send
// the node, and the client parameters to put in subscriptions.
func BuildInbound(spec InboundSpec) (*store.Inbound, error) {
	if spec.Tag == "" {
		return nil, invalid("inbound tag is required")
	}
	if spec.Port < 1 || spec.Port > 65535 {
		return nil, invalid("port %d out of range", spec.Port)
	}

	// "::" listens on both stacks. A node that only has IPv4 still binds.
	in := singbox.Inbound{Tag: spec.Tag, Listen: "::", ListenPort: spec.Port}
	var client ClientParams

	switch spec.Protocol {
	case store.ProtoVLESS:
		host, port, err := splitHandshake(spec.Handshake)
		if err != nil {
			return nil, err
		}
		priv, pub, err := newRealityKeypair()
		if err != nil {
			return nil, err
		}
		shortID := NewShortID()

		in.Type = "vless"
		in.TLS = &singbox.TLS{
			Enabled:    true,
			ServerName: host,
			Reality: &singbox.Reality{
				Enabled:    true,
				Handshake:  singbox.Handshake{Server: host, ServerPort: port},
				PrivateKey: priv,
				ShortID:    []string{shortID},
			},
		}
		client = ClientParams{SNI: host, PBK: pub, SID: shortID, FP: "chrome",
			Flow: FlowVision}

	case store.ProtoAnyTLS:
		// Blank means "wherever the installer put it". Refusing instead would
		// make the field look mandatory when the answer is the same on every
		// node the installer touched.
		if spec.CertPath == "" {
			spec.CertPath = DefaultCertPath
		}
		if spec.KeyPath == "" {
			spec.KeyPath = DefaultKeyPath
		}
		if spec.ServerName == "" {
			return nil, invalid("anytls needs a server name")
		}
		in.Type = "anytls"
		in.TLS = &singbox.TLS{
			Enabled:         true,
			ServerName:      spec.ServerName,
			CertificatePath: spec.CertPath,
			KeyPath:         spec.KeyPath,
		}
		// No multiplex block: AnyTLS multiplexes on its own, and configuring
		// smux on top of it breaks the connection.
		client = ClientParams{SNI: spec.ServerName, FP: "chrome"}

	case store.ProtoShadowsocks:
		psk := NewSSPassword()
		in.Type = "shadowsocks"
		in.Method = SSMethod
		in.Password = psk
		in.Network = []string{"tcp", "udp"}
		client = ClientParams{Method: SSMethod, ServerPSK: psk}

	default:
		return nil, invalid("unsupported protocol %q", spec.Protocol)
	}

	cfgJSON, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshal inbound: %w", err)
	}
	clientJSON, err := json.Marshal(client)
	if err != nil {
		return nil, fmt.Errorf("marshal client params: %w", err)
	}

	return &store.Inbound{
		Tag: spec.Tag, Protocol: spec.Protocol, Port: spec.Port,
		Config: string(cfgJSON), Client: string(clientJSON), Enabled: true,
	}, nil
}

// newRealityKeypair returns (private, public), both base64url without padding.
//
// crypto/ecdh does this natively, so there is no shelling out to openssl and no
// hand-parsing of DER to find the raw scalar. The encoding is not incidental:
// sing-box decodes the private key with base64.RawURLEncoding, and every client
// expects the same form for the public key.
func newRealityKeypair() (priv, pub string, err error) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate x25519 key: %w", err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(key.Bytes()), enc.EncodeToString(key.PublicKey().Bytes()), nil
}

// splitHandshake accepts "host" or "host:port", defaulting to 443.
func splitHandshake(s string) (host string, port int, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		s = DefaultHandshake
	}
	if !strings.Contains(s, ":") {
		return s, 443, nil
	}
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		return "", 0, invalid("handshake target %q is not host:port", s)
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return "", 0, invalid("handshake target %q has a bad port", s)
	}
	if h == "" {
		return "", 0, invalid("handshake target %q has no host", s)
	}
	return h, n, nil
}

// ParseClient reads the client parameters back out of a stored inbound.
func ParseClient(in *store.Inbound) (ClientParams, error) {
	var c ClientParams
	if err := json.Unmarshal([]byte(in.Client), &c); err != nil {
		return c, fmt.Errorf("inbound %s: parse client params: %w", in.Tag, err)
	}
	return c, nil
}

// ParseConfig reads the sing-box inbound back out of a stored inbound.
func ParseConfig(in *store.Inbound) (singbox.Inbound, error) {
	var sb singbox.Inbound
	if err := json.Unmarshal([]byte(in.Config), &sb); err != nil {
		return sb, fmt.Errorf("inbound %s: parse config: %w", in.Tag, err)
	}
	return sb, nil
}
