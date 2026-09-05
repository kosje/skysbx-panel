package service

import (
	"crypto/ecdh"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/kosje/skysbx-panel/internal/store"
)

func TestBuildVLESSReality(t *testing.T) {
	in, err := BuildInbound(InboundSpec{
		Protocol: store.ProtoVLESS, Tag: "vless-tokyo", Port: 443,
		Handshake: "www.microsoft.com:443",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	sb, err := ParseConfig(in)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if sb.Type != "vless" || sb.ListenPort != 443 || sb.Listen != "::" {
		t.Fatalf("unexpected inbound: %+v", sb)
	}
	if sb.TLS == nil || sb.TLS.Reality == nil || !sb.TLS.Reality.Enabled {
		t.Fatal("reality not configured")
	}
	if sb.TLS.Reality.Handshake.Server != "www.microsoft.com" ||
		sb.TLS.Reality.Handshake.ServerPort != 443 {
		t.Fatalf("handshake: %+v", sb.TLS.Reality.Handshake)
	}
	// The stored config must carry no users; they arrive separately so a user
	// change never rebuilds the listener.
	if len(sb.Users) != 0 {
		t.Fatalf("stored config should have no users, got %d", len(sb.Users))
	}

	c, err := ParseClient(in)
	if err != nil {
		t.Fatalf("parse client: %v", err)
	}
	if c.Flow != "xtls-rprx-vision" || c.SNI != "www.microsoft.com" || c.SID == "" {
		t.Fatalf("client params: %+v", c)
	}
}

// The private key sing-box gets and the public key clients get must be the same
// keypair, or the inbound silently rejects every connection. Recomputing the
// public key from the private one here is the only way to prove they match.
func TestRealityKeypairMatches(t *testing.T) {
	in, err := BuildInbound(InboundSpec{
		Protocol: store.ProtoVLESS, Tag: "t", Port: 443, Handshake: "example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	sb, _ := ParseConfig(in)
	c, _ := ParseClient(in)

	privRaw, err := base64.RawURLEncoding.DecodeString(sb.TLS.Reality.PrivateKey)
	if err != nil {
		t.Fatalf("private key is not unpadded base64url: %v", err)
	}
	if len(privRaw) != 32 {
		t.Fatalf("private key decoded to %d bytes, want 32", len(privRaw))
	}

	key, err := ecdh.X25519().NewPrivateKey(privRaw)
	if err != nil {
		t.Fatalf("private key rejected by crypto/ecdh: %v", err)
	}
	want := base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
	if c.PBK != want {
		t.Fatalf("public key does not match private key:\n  stored  %s\n  derived %s", c.PBK, want)
	}
}

// sing-box decodes the Reality private key with base64.RawURLEncoding. Padding
// makes it fail outright, and '+' or '/' from standard base64 would too.
func TestRealityPrivateKeyEncoding(t *testing.T) {
	for i := 0; i < 50; i++ {
		in, err := BuildInbound(InboundSpec{
			Protocol: store.ProtoVLESS, Tag: "t", Port: 443, Handshake: "example.com",
		})
		if err != nil {
			t.Fatal(err)
		}
		sb, _ := ParseConfig(in)
		k := sb.TLS.Reality.PrivateKey
		if strings.ContainsAny(k, "=+/") {
			t.Fatalf("private key is not raw base64url: %q", k)
		}
	}
}

func TestBuildAnyTLS(t *testing.T) {
	_, err := BuildInbound(InboundSpec{
		Protocol: store.ProtoAnyTLS, Tag: "anytls-tokyo", Port: 8443,
	})
	if err == nil {
		t.Fatal("expected a failure without certificate paths")
	}

	in, err := BuildInbound(InboundSpec{
		Protocol: store.ProtoAnyTLS, Tag: "anytls-tokyo", Port: 8443,
		CertPath: "/opt/skysbx/cert.pem", KeyPath: "/opt/skysbx/key.pem",
		ServerName: "jp.example.com",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	sb, _ := ParseConfig(in)
	if sb.Type != "anytls" || sb.TLS == nil || sb.TLS.CertificatePath == "" {
		t.Fatalf("unexpected inbound: %+v", sb)
	}
	if sb.TLS.Reality != nil {
		t.Fatal("anytls must not carry a reality block")
	}
	// AnyTLS multiplexes on its own; the config must stay free of any mux
	// settings, and the marshalled JSON is the thing that actually reaches the
	// node, so assert on that.
	if strings.Contains(in.Config, "multiplex") || strings.Contains(in.Config, "smux") {
		t.Fatalf("anytls config carries multiplex settings: %s", in.Config)
	}
}

func TestBuildShadowsocks(t *testing.T) {
	in, err := BuildInbound(InboundSpec{
		Protocol: store.ProtoShadowsocks, Tag: "ss-tokyo", Port: 8388,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	sb, _ := ParseConfig(in)
	if sb.Method != SSMethod {
		t.Fatalf("method %q, want %q", sb.Method, SSMethod)
	}
	raw, err := base64.StdEncoding.DecodeString(sb.Password)
	if err != nil || len(raw) != 32 {
		t.Fatalf("server PSK must be base64 of 32 bytes: %q (%v)", sb.Password, err)
	}

	c, _ := ParseClient(in)
	if c.ServerPSK != sb.Password {
		t.Fatal("client params must carry the same server PSK the node gets")
	}
	// UDP matters: Shadowsocks is the only one of the three that carries it.
	if len(sb.Network) != 2 {
		t.Fatalf("expected tcp and udp, got %v", sb.Network)
	}
}

func TestSplitHandshake(t *testing.T) {
	cases := []struct {
		in       string
		wantHost string
		wantPort int
		wantErr  bool
	}{
		{"www.microsoft.com", "www.microsoft.com", 443, false},
		{"www.microsoft.com:443", "www.microsoft.com", 443, false},
		{"example.com:8443", "example.com", 8443, false},
		{"  example.com  ", "example.com", 443, false},
		{"", "www.microsoft.com", 443, false}, // falls back to the default
		{"example.com:0", "", 0, true},
		{"example.com:abc", "", 0, true},
		{":443", "", 0, true},
	}
	for _, c := range cases {
		h, p, err := splitHandshake(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("splitHandshake(%q) should have failed", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("splitHandshake(%q): %v", c.in, err)
		} else if h != c.wantHost || p != c.wantPort {
			t.Errorf("splitHandshake(%q) = %s,%d want %s,%d", c.in, h, p, c.wantHost, c.wantPort)
		}
	}
}

func TestBuildRejectsBadInput(t *testing.T) {
	cases := []InboundSpec{
		{Protocol: store.ProtoVLESS, Tag: "", Port: 443},
		{Protocol: store.ProtoVLESS, Tag: "t", Port: 0},
		{Protocol: store.ProtoVLESS, Tag: "t", Port: 70000},
		{Protocol: "trojan", Tag: "t", Port: 443},
	}
	for _, c := range cases {
		if _, err := BuildInbound(c); err == nil {
			t.Errorf("expected failure for %+v", c)
		}
	}
}
