package service

import (
	"path/filepath"
	"testing"

	"github.com/kosje/skysbx-panel/internal/store"
)

// Editing an inbound must carry the generated secrets over untouched. Minting
// new ones would stop every client holding the old subscription, with nothing
// in any log to say why — a wrong Reality key is indistinguishable from a probe,
// and the connection is simply dropped.
func TestEditingAnInboundKeepsItsKeys(t *testing.T) {
	svc, nodeID := editFixture(t)

	for _, tc := range []struct {
		name  string
		spec  InboundSpec
		edit  func(*InboundEdit)
		check func(t *testing.T, before, after ClientParams)
	}{{
		name: "vless keeps the reality key pair and short id",
		spec: InboundSpec{Protocol: store.ProtoVLESS, Port: 443,
			Handshake: DefaultHandshake},
		edit: func(e *InboundEdit) { e.Handshake = "www.apple.com:443" },
		check: func(t *testing.T, before, after ClientParams) {
			if after.PBK != before.PBK {
				t.Error("the reality public key changed")
			}
			if after.SID != before.SID {
				t.Error("the reality short id changed")
			}
			// The handshake host is also the SNI; they have to move together or
			// Reality rejects every connection.
			if after.SNI != "www.apple.com" {
				t.Errorf("SNI is %q, want the new handshake host", after.SNI)
			}
		},
	}, {
		name: "shadowsocks keeps the server psk",
		spec: InboundSpec{Protocol: store.ProtoShadowsocks, Port: 8388},
		edit: func(e *InboundEdit) {},
		check: func(t *testing.T, before, after ClientParams) {
			if after.ServerPSK != before.ServerPSK {
				t.Error("the server PSK changed; every client's key is now wrong")
			}
			if after.Method != before.Method {
				t.Error("the method changed")
			}
		},
	}, {
		name: "anytls picks up new paths",
		spec: InboundSpec{Protocol: store.ProtoAnyTLS, Port: 8443,
			ServerName: "a.example.com"},
		edit: func(e *InboundEdit) {
			e.ServerName = "b.example.com"
			e.CertPath = "/tmp/c.pem"
			e.KeyPath = "/tmp/k.pem"
		},
		check: func(t *testing.T, before, after ClientParams) {
			if after.SNI != "b.example.com" {
				t.Errorf("SNI is %q, want the new server name", after.SNI)
			}
		},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			in, err := svc.CreateInbound(nodeID, tc.spec)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			before, err := ParseClient(in)
			if err != nil {
				t.Fatalf("parse client: %v", err)
			}

			e := InboundEdit{Port: 9999, Handshake: tc.spec.Handshake,
				ServerName: tc.spec.ServerName}
			tc.edit(&e)

			edited, err := svc.EditInbound(in.ID, e)
			if err != nil {
				t.Fatalf("edit: %v", err)
			}
			if edited.Port != 9999 {
				t.Errorf("port is %d, want 9999", edited.Port)
			}
			if edited.Tag != in.Tag {
				t.Errorf("tag changed from %q to %q", in.Tag, edited.Tag)
			}
			if edited.Protocol != in.Protocol {
				t.Errorf("protocol changed from %q to %q", in.Protocol, edited.Protocol)
			}

			after, err := ParseClient(edited)
			if err != nil {
				t.Fatalf("parse client: %v", err)
			}
			tc.check(t, before, after)

			// The stored sing-box config has to agree with the row, or the node
			// would go on listening where the panel says it does not.
			sb, err := ParseConfig(edited)
			if err != nil {
				t.Fatalf("parse config: %v", err)
			}
			if sb.ListenPort != 9999 {
				t.Errorf("config listen_port is %d, want 9999", sb.ListenPort)
			}
		})
	}
}

// A user's own credentials are not stored on the inbound, so an edit must not
// disturb them either — the check is that the subscription still resolves.
func TestEditingAnInboundLeavesUsersAlone(t *testing.T) {
	svc, nodeID := editFixture(t)

	in, err := svc.CreateInbound(nodeID, InboundSpec{
		Protocol: store.ProtoShadowsocks, Port: 8388})
	if err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	u, err := svc.CreateUser(NewUser{Name: "alice"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	before, err := svc.NodeUsers(nodeID)
	if err != nil {
		t.Fatalf("users: %v", err)
	}
	if _, err := svc.EditInbound(in.ID, InboundEdit{Port: 9001}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	after, err := svc.NodeUsers(nodeID)
	if err != nil {
		t.Fatalf("users: %v", err)
	}

	if len(after[in.Tag]) != len(before[in.Tag]) {
		t.Fatalf("user count changed from %d to %d",
			len(before[in.Tag]), len(after[in.Tag]))
	}
	if len(after[in.Tag]) == 0 {
		t.Fatal("no users on the inbound")
	}
	if after[in.Tag][0].Password != before[in.Tag][0].Password {
		t.Errorf("%s's password changed", u.Name)
	}
}

func editFixture(t *testing.T) (*Service, int64) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	svc := New(st)
	node, _, err := svc.CreateNode("tokyo", "jp.example.com", "JP")
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	return svc, node.ID
}

// The relay address is parsed twice: once here to reject a typo while the
// operator is still looking at the field, and once by the subscription
// generator, which falls back rather than failing. The two must agree on what
// is acceptable, or a value saved here comes out meaning something else.
func TestRelayAddressValidation(t *testing.T) {
	for _, tc := range []struct {
		address string
		ok      bool
	}{
		{"", true},
		{"relay.example.net", true},
		{"relay.example.net:443", true},
		{"203.0.113.9", true},
		{"203.0.113.9:8443", true},
		{"[2001:db8::1]:443", true},
		{"2001:db8::1", true}, // bare IPv6: colons, but no port
		{"relay.example.net:abc", false},
		{"relay.example.net:0", false},
		{"relay.example.net:99999", false},
		{":443", false},
	} {
		err := CheckRelayAddress(tc.address)
		if tc.ok && err != nil {
			t.Errorf("%q rejected: %v", tc.address, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%q accepted, want a complaint", tc.address)
		}
	}
}
