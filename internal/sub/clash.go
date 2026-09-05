package sub

import (
	"gopkg.in/yaml.v3"

	"github.com/kosje/skysbx-panel/internal/store"
)

// clashProxy is mihomo's proxy schema. Field order here is the order they
// appear in the output, which matters only for readability.
//
// Written through a real YAML marshaller rather than a template: every
// credential this emits is base64 or base64url, so it contains '+', '/' and
// '=' — characters that a hand-rolled emitter gets wrong exactly often enough
// to be hard to notice.
type clashProxy struct {
	Name   string `yaml:"name"`
	Type   string `yaml:"type"`
	Server string `yaml:"server"`
	Port   int    `yaml:"port"`
	UDP    bool   `yaml:"udp"`

	UUID    string `yaml:"uuid,omitempty"`
	Flow    string `yaml:"flow,omitempty"`
	Network string `yaml:"network,omitempty"`
	TLS     bool   `yaml:"tls,omitempty"`

	Password string `yaml:"password,omitempty"`
	Cipher   string `yaml:"cipher,omitempty"`

	SNI               string            `yaml:"servername,omitempty"`
	AnyTLSSNI         string            `yaml:"sni,omitempty"`
	ClientFingerprint string            `yaml:"client-fingerprint,omitempty"`
	RealityOpts       *clashRealityOpts `yaml:"reality-opts,omitempty"`
}

type clashRealityOpts struct {
	PublicKey string `yaml:"public-key"`
	ShortID   string `yaml:"short-id,omitempty"`
}

type clashGroup struct {
	Name     string   `yaml:"name"`
	Type     string   `yaml:"type"`
	Proxies  []string `yaml:"proxies"`
	URL      string   `yaml:"url,omitempty"`
	Interval int      `yaml:"interval,omitempty"`
}

type clashConfig struct {
	MixedPort   int          `yaml:"mixed-port"`
	AllowLAN    bool         `yaml:"allow-lan"`
	Mode        string       `yaml:"mode"`
	LogLevel    string       `yaml:"log-level"`
	Proxies     []clashProxy `yaml:"proxies"`
	ProxyGroups []clashGroup `yaml:"proxy-groups"`
	Rules       []string     `yaml:"rules"`
}

// Clash renders a mihomo configuration.
//
// AnyTLS is included. mihomo supports it; the older Clash forks do not and will
// reject the file outright rather than skip the proxy. Anyone on one of those
// should use the sing-box or share-link format instead.
func Clash(entries []Entry) ([]byte, error) {
	proxies := make([]clashProxy, 0, len(entries))
	names := make([]string, 0, len(entries))

	for _, e := range entries {
		p := clashProxy{
			Name: e.Name, Server: e.Address, Port: e.Port, UDP: true,
			ClientFingerprint: fpOr(e.FP),
		}
		switch e.Protocol {
		case store.ProtoVLESS:
			p.Type = "vless"
			p.UUID = e.UUID
			p.Flow = e.Flow
			p.Network = "tcp"
			p.TLS = true
			p.SNI = e.SNI
			p.RealityOpts = &clashRealityOpts{PublicKey: e.PBK, ShortID: e.SID}
		case store.ProtoAnyTLS:
			p.Type = "anytls"
			p.Password = e.Password
			p.AnyTLSSNI = e.SNI
		case store.ProtoShadowsocks:
			p.Type = "ss"
			p.Cipher = e.Method
			p.Password = e.Password
			p.ClientFingerprint = "" // meaningless without TLS
		default:
			continue
		}
		proxies = append(proxies, p)
		names = append(names, e.Name)
	}

	groups := []clashGroup{}
	rules := []string{"MATCH,DIRECT"}
	if len(names) > 0 {
		groups = []clashGroup{
			{Name: "PROXY", Type: "select", Proxies: append([]string{"AUTO"}, names...)},
			{Name: "AUTO", Type: "url-test", Proxies: names,
				URL: "https://www.gstatic.com/generate_204", Interval: 180},
		}
		rules = []string{"GEOIP,PRIVATE,DIRECT,no-resolve", "MATCH,PROXY"}
	}

	return yaml.Marshal(clashConfig{
		MixedPort: 7890, AllowLAN: false, Mode: "rule", LogLevel: "warning",
		Proxies: proxies, ProxyGroups: groups, Rules: rules,
	})
}
