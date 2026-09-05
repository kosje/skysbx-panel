package sub

import (
	"encoding/json"
	"net"

	"github.com/kosje/skysbx-panel/internal/singbox"
	"github.com/kosje/skysbx-panel/internal/store"
)

// Tags for the outbounds the panel always emits.
const (
	tagProxy  = "proxy"
	tagAuto   = "auto"
	tagDirect = "direct"
)

// SingBox renders a complete sing-box client configuration.
//
// The proxy outbounds are the point of this file; the DNS, inbound and route
// blocks are a deliberately small working default rather than an opinion about
// how anyone should route their traffic. A user who wants more is better served
// editing the result than having the panel grow a template language.
func SingBox(entries []Entry) ([]byte, error) {
	outbounds := make([]singbox.ClientOutbound, 0, len(entries)+3)
	names := make([]string, 0, len(entries))

	for _, e := range entries {
		ob := singbox.ClientOutbound{
			Tag: e.Name, Server: e.Address, ServerPort: e.Port,
		}
		switch e.Protocol {
		case store.ProtoVLESS:
			ob.Type = "vless"
			ob.UUID = e.UUID
			ob.Flow = e.Flow
			ob.TLS = &singbox.ClientTLS{
				Enabled:    true,
				ServerName: e.SNI,
				// Reality hides behind a browser-shaped handshake, so uTLS is
				// not optional here.
				UTLS:    &singbox.UTLS{Enabled: true, Fingerprint: fpOr(e.FP)},
				Reality: &singbox.ClientReality{Enabled: true, PublicKey: e.PBK, ShortID: e.SID},
			}
		case store.ProtoAnyTLS:
			ob.Type = "anytls"
			ob.Password = e.Password
			ob.TLS = &singbox.ClientTLS{
				Enabled:    true,
				ServerName: e.SNI,
				UTLS:       &singbox.UTLS{Enabled: true, Fingerprint: fpOr(e.FP)},
			}
		case store.ProtoShadowsocks:
			ob.Type = "shadowsocks"
			ob.Method = e.Method
			ob.Password = e.Password
		default:
			continue
		}
		outbounds = append(outbounds, ob)
		names = append(names, e.Name)
	}

	// A selector with the urltest inside it gives both automatic choice and a
	// manual override, which is what every client UI expects to find.
	auto := singbox.ClientOutbound{
		Type: "urltest", Tag: tagAuto, Outbounds: names,
		URL: "https://www.gstatic.com/generate_204", Interval: "3m",
	}
	proxy := singbox.ClientOutbound{
		Type: "selector", Tag: tagProxy,
		Outbounds: append([]string{tagAuto}, names...), Default: tagAuto,
	}
	if len(names) == 0 {
		// An inactive or unassigned user still gets a parseable config. A
		// selector with no members makes sing-box refuse to start, which looks
		// to the user like a broken client rather than an expired account.
		auto.Outbounds = []string{tagDirect}
		proxy.Outbounds = []string{tagDirect}
		proxy.Default = tagDirect
	}
	outbounds = append(outbounds, auto, proxy,
		singbox.ClientOutbound{Type: tagDirect, Tag: tagDirect})

	dns := &singbox.DNS{
		Servers: []singbox.DNSServer{
			{Type: "https", Tag: "remote", Server: "1.1.1.1", Detour: tagProxy},
			// No detour: that already means direct, and naming the empty
			// direct outbound explicitly is rejected as meaningless.
			{Type: "https", Tag: "local", Server: "223.5.5.5"},
		},
		Final: "remote",
	}
	// Resolving a server's own hostname through the proxy that has not been
	// dialled yet is a loop. Send those names to the local resolver.
	if hosts := serverDomains(entries); len(hosts) > 0 {
		dns.Rules = []singbox.DNSRule{{Domain: hosts, Server: "local"}}
	}

	cfg := singbox.ClientConfig{
		Log: &singbox.Log{Level: "warn"},
		DNS: dns,
		Inbounds: []singbox.ClientInbound{{
			Type: "mixed", Tag: "mixed-in", Listen: "127.0.0.1", ListenPort: 2080,
		}},
		Outbounds: outbounds,
		Route: &singbox.Route{
			Rules: []singbox.RouteRule{
				{IPIsPriv: true, Outbound: tagDirect},
			},
			Final: tagProxy,
			Auto:  true,
			// Which resolver turns a dialled name into an address. "local" so
			// that a node addressed by name is resolved outside the tunnel that
			// name is the far end of.
			DefaultDomainResolver: &singbox.DomainResolver{Server: "local"},
		},
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// serverDomains is the set of node addresses that are names rather than
// literal addresses, deduplicated and in first-seen order.
func serverDomains(entries []Entry) []string {
	seen := make(map[string]bool, len(entries))
	var hosts []string
	for _, e := range entries {
		if e.Address == "" || net.ParseIP(e.Address) != nil || seen[e.Address] {
			continue
		}
		seen[e.Address] = true
		hosts = append(hosts, e.Address)
	}
	return hosts
}

func fpOr(fp string) string {
	if fp == "" {
		return "chrome"
	}
	return fp
}
