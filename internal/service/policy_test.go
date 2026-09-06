package service

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/kosje/skysbx-panel/internal/store"
)

// Sniffing has to be the first rule. Every rule below it matches on what
// sniffing found, and one placed above matches nothing — silently, which is the
// worst way for a block to fail.
func TestSniffingComesFirst(t *testing.T) {
	rules := Policy{BlockBitTorrent: true, BlockSpeedtest: true,
		BlockedDomains: []string{"emby.example.org"}}.routeRules(nil)

	if len(rules) == 0 {
		t.Fatal("no rules")
	}
	if rules[0].Action != "sniff" {
		t.Fatalf("first rule is %q, want sniff", rules[0].Action)
	}
	for _, r := range rules[1:] {
		if r.Action != "reject" {
			t.Errorf("rule after the sniffer is %q, want reject", r.Action)
		}
	}
	// A protocol match is worthless without the sniffer that sets it.
	if !slices.Contains(rules[0].Sniffer, "bittorrent") {
		t.Error("bittorrent is blocked but not sniffed for")
	}
}

// Sniffing costs bytes read and compared on the first packet of every
// connection. A node with no policy must not pay for it.
func TestNoPolicyMeansNoRules(t *testing.T) {
	if r := (Policy{}).routeRules(nil); len(r) != 0 {
		t.Errorf("an empty policy produced %d rules", len(r))
	}
	// And a domain list alone should not sniff for bittorrent.
	r := Policy{BlockedDomains: []string{"a.example"}}.routeRules(nil)
	if len(r) == 0 {
		t.Fatal("a domain list produced no rules")
	}
	if slices.Contains(r[0].Sniffer, "bittorrent") {
		t.Error("sniffing for bittorrent when nothing matches on it")
	}
}

// The blocklist is pasted by a human. Anything they are likely to paste has to
// come out as a bare hostname, because sing-box matches suffixes literally and
// "https://emby.example.org/web" matches nothing at all.
func TestBlocklistIsNormalised(t *testing.T) {
	got := cleanDomains([]string{
		"  Emby.Example.ORG  ",
		"https://pt.example.net/browse.php",
		"*.tracker.example",
		".leading.dot",
		"a.example, b.example",
		"emby.example.org", // duplicate, different case above
		"",
		"host.example:8080",
	})
	want := []string{
		"emby.example.org", "pt.example.net", "tracker.example",
		"leading.dot", "a.example", "b.example", "host.example",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

// A policy stored as unparseable JSON must not take every node's routing with
// it. The safe reading of a corrupt policy is no policy, not a crash on the
// next configuration push.
func TestACorruptPolicyIsNoPolicy(t *testing.T) {
	svc, _ := editFixture(t)
	if err := svc.Store().SetSetting(settingPolicy, "{not json"); err != nil {
		t.Fatal(err)
	}
	p, err := svc.Policy()
	if err != nil {
		t.Fatalf("Policy: %v", err)
	}
	if p.BlockBitTorrent || p.BlockSpeedtest || len(p.BlockedDomains) != 0 {
		t.Errorf("a corrupt policy came back as %+v", p)
	}
}

// The rules have to reach the node, in the configuration rather than the user
// list: they are a property of the node's routing, not of who is connected.
func TestPolicyReachesThePushedConfig(t *testing.T) {
	svc, nodeID := editFixture(t)
	if _, err := svc.CreateInbound(nodeID, InboundSpec{
		Protocol: store.ProtoShadowsocks, Port: 8388}); err != nil {
		t.Fatal(err)
	}

	cfg, err := svc.NodeConfig(nodeID)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if cfg.Route != nil {
		t.Error("a node with no policy was sent routing rules")
	}

	if err := svc.SetPolicy(Policy{BlockBitTorrent: true,
		BlockedDomains: []string{"EMBY.example.org"}}); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	cfg, err = svc.NodeConfig(nodeID)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if cfg.Route == nil || len(cfg.Route.Rules) < 3 {
		t.Fatalf("routing rules did not reach the config: %+v", cfg.Route)
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"action":"sniff"`, `"bittorrent"`,
		`"action":"reject"`, `"emby.example.org"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("pushed config does not contain %s", want)
		}
	}
	// Lowercased on the way in, so a pasted capital does not silently miss.
	if strings.Contains(string(raw), "EMBY") {
		t.Error("the blocklist was not normalised before being pushed")
	}
}
