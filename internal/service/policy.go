package service

import (
	"encoding/json"
	"strings"

	"github.com/kosje/skysbx-panel/internal/singbox"
)

const settingPolicy = "policy"

// Policy is what a node refuses to carry. It applies to every node, because a
// user denied BitTorrent on one node and allowed it on another has not been
// denied anything.
type Policy struct {
	// BlockBitTorrent rejects connections sing-box sniffs as BitTorrent.
	//
	// Partial by construction, and worth being precise about: the TCP sniffer
	// matches the plaintext handshake, which encrypted BitTorrent — the default
	// in most clients — does not send. What it does still catch is uTP, whose
	// framing stays in the clear even when the payload is encrypted, and UDP
	// tracker announces. In practice that is most of the traffic and all of the
	// peer discovery, but it is not a wall.
	BlockBitTorrent bool `json:"block_bittorrent"`

	// BlockSpeedtest rejects the well-known speed test sites. Someone running
	// one continuously costs more than everyone else combined, and no client
	// needs them.
	BlockSpeedtest bool `json:"block_speedtest"`

	// BlockedDomains is the operator's own list, one per line. Suffix match: an
	// entry blocks the name and everything under it.
	BlockedDomains []string `json:"blocked_domains,omitempty"`
}

// speedtestDomains are the hosts that exist to move as much data as possible
// as fast as possible. Suffixes, so subdomains go with them.
var speedtestDomains = []string{
	"speedtest.net",
	"ooklaserver.net",
	"speedtestcustom.com",
	"fast.com",
	"speed.cloudflare.com",
	"librespeed.org",
	"speedof.me",
	"testmy.net",
	"nperf.com",
	"speed.measurementlab.net",
	"proof.ovh.net",
	"speedtest.tele2.net",
}

// SpeedtestDomains is the built-in list, so the UI can show what turning that
// switch on actually blocks rather than asking the operator to trust it.
func SpeedtestDomains() []string { return speedtestDomains }

func (s *Service) Policy() (Policy, error) {
	raw, err := s.st.Setting(settingPolicy)
	if err != nil || raw == "" {
		return Policy{}, err
	}
	var p Policy
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		// A policy that will not parse must not take every node's routing with
		// it: the safe reading of a corrupt policy is "no policy".
		return Policy{}, nil
	}
	return p, nil
}

// SetPolicy stores it and pushes a new configuration to every node.
//
// A configuration push rebuilds listeners and drops live connections. That is
// the right price here: a policy that only applies to connections made after
// the next unrelated edit is not a policy.
func (s *Service) SetPolicy(p Policy) error {
	p.BlockedDomains = cleanDomains(p.BlockedDomains)
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := s.st.SetSetting(settingPolicy, string(raw)); err != nil {
		return err
	}
	nodes, err := s.st.Nodes()
	if err != nil {
		return err
	}
	for _, n := range nodes {
		s.notify.ConfigChanged(n.ID)
	}
	return nil
}

// cleanDomains normalises a pasted list: one per line or comma separated, no
// scheme, no leading dot, lowercase, deduplicated.
func cleanDomains(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range in {
		for _, field := range strings.FieldsFunc(line, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t'
		}) {
			d := strings.ToLower(strings.TrimSpace(field))
			d = strings.TrimPrefix(d, "http://")
			d = strings.TrimPrefix(d, "https://")
			d = strings.TrimPrefix(d, "*.")
			d = strings.Trim(d, ".")
			if i := strings.IndexAny(d, "/:"); i >= 0 {
				d = d[:i]
			}
			if d == "" || seen[d] {
				continue
			}
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// routeRules turns a policy into the rules a node runs.
//
// Order matters and is not adjustable: sniffing has to come first, because
// every rule below it matches on what sniffing found. A rule that matches on
// protocol above the sniff rule matches nothing, silently.
func (p Policy) routeRules() []singbox.ServerRouteRule {
	var rules []singbox.ServerRouteRule
	if !p.BlockBitTorrent && !p.BlockSpeedtest && len(p.BlockedDomains) == 0 {
		return nil
	}

	// Only the sniffers a rule below actually needs. Each one is bytes read and
	// compared on the first packet of every connection, and sniffing for
	// protocols nobody is matching on is pure cost.
	sniffers := []string{"tls", "http"} // domain rules need a hostname
	if p.BlockBitTorrent {
		sniffers = append(sniffers, "bittorrent", "quic")
	}
	rules = append(rules, singbox.ServerRouteRule{
		Action: "sniff", Sniffer: sniffers, Timeout: "300ms",
	})

	if p.BlockBitTorrent {
		rules = append(rules, singbox.ServerRouteRule{
			Protocol: []string{"bittorrent"}, Action: "reject",
		})
	}
	if p.BlockSpeedtest {
		rules = append(rules, singbox.ServerRouteRule{
			DomainSuffix: speedtestDomains, Action: "reject",
		})
	}
	if len(p.BlockedDomains) > 0 {
		rules = append(rules, singbox.ServerRouteRule{
			DomainSuffix: p.BlockedDomains, Action: "reject",
		})
	}
	return rules
}
