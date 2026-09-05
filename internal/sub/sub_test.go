package sub

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/kosje/skysbx-panel/internal/service"
	"github.com/kosje/skysbx-panel/internal/store"
)

// fixture builds one node with all three protocols and one active user.
func fixture(t *testing.T) (*store.User, []*store.Node, []*store.Inbound) {
	t.Helper()

	node := &store.Node{ID: 1, Name: "tokyo", Address: "jp.example.com",
		Country: "JP", Enabled: true}

	specs := []service.InboundSpec{
		{Protocol: store.ProtoVLESS, Tag: "vless-tokyo", Port: 443,
			Handshake: service.DefaultHandshake},
		{Protocol: store.ProtoAnyTLS, Tag: "anytls-tokyo", Port: 8443,
			CertPath: "/c.pem", KeyPath: "/k.pem", ServerName: "jp.example.com"},
		{Protocol: store.ProtoShadowsocks, Tag: "ss-tokyo", Port: 8388},
	}
	var inbounds []*store.Inbound
	for i, s := range specs {
		in, err := service.BuildInbound(s)
		if err != nil {
			t.Fatalf("build %s: %v", s.Protocol, err)
		}
		in.ID = int64(i + 1)
		in.NodeID = node.ID
		inbounds = append(inbounds, in)
	}

	u := &store.User{
		ID: 1, Name: "alice", Enabled: true,
		VlessUUID:  service.NewUUID(),
		Password:   service.NewPassword(),
		SSPassword: service.NewSSPassword(),
	}
	return u, []*store.Node{node}, inbounds
}

func TestBuildCoversEveryInbound(t *testing.T) {
	u, nodes, inbounds := fixture(t)
	entries, err := Build(u, nodes, inbounds, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	for _, e := range entries {
		if e.Address != "jp.example.com" || e.Name == "" {
			t.Errorf("entry %+v is missing address or name", e)
		}
	}
}

// Shadowsocks 2022 authenticates with both halves of the key pair joined by a
// colon. The server half alone authenticates as nobody, and bills to nobody.
func TestShadowsocksCarriesBothKeyHalves(t *testing.T) {
	u, nodes, inbounds := fixture(t)
	entries, _ := Build(u, nodes, inbounds, nil)

	var ss *Entry
	for i := range entries {
		if entries[i].Protocol == store.ProtoShadowsocks {
			ss = &entries[i]
		}
	}
	if ss == nil {
		t.Fatal("no shadowsocks entry")
	}
	parts := strings.Split(ss.Password, ":")
	if len(parts) != 2 {
		t.Fatalf("password %q is not serverPSK:userPSK", ss.Password)
	}
	if parts[1] != u.SSPassword {
		t.Errorf("user half = %q, want %q", parts[1], u.SSPassword)
	}
	for i, half := range parts {
		raw, err := base64.StdEncoding.DecodeString(half)
		if err != nil || len(raw) != 32 {
			t.Errorf("half %d is not base64 of 32 bytes: %q", i, half)
		}
	}
}

// An inactive user gets an empty subscription rather than an error: their
// client should stop connecting, not retry a failing HTTP request forever.
func TestInactiveUserGetsEmptySubscription(t *testing.T) {
	u, nodes, inbounds := fixture(t)
	past := time.Now().Add(-time.Hour)
	u.ExpiresAt = &past

	entries, err := Build(u, nodes, inbounds, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expired user got %d entries", len(entries))
	}
}

func TestDisabledNodeAndInboundAreSkipped(t *testing.T) {
	u, nodes, inbounds := fixture(t)

	inbounds[0].Enabled = false
	entries, _ := Build(u, nodes, inbounds, nil)
	if len(entries) != 2 {
		t.Fatalf("disabling an inbound left %d entries, want 2", len(entries))
	}

	nodes[0].Enabled = false
	entries, _ = Build(u, nodes, inbounds, nil)
	if len(entries) != 0 {
		t.Fatalf("disabling the node left %d entries", len(entries))
	}
}

func TestRestrictionsAreHonoured(t *testing.T) {
	u, nodes, inbounds := fixture(t)
	entries, _ := Build(u, nodes, inbounds, map[int64]bool{inbounds[1].ID: true})
	if len(entries) != 1 || entries[0].Protocol != store.ProtoAnyTLS {
		t.Fatalf("restriction not honoured: %+v", entries)
	}
}

// ── share links ─────────────────────────────────────────────────────────────

func TestShareLinksParseAndCarryTheRightFields(t *testing.T) {
	u, nodes, inbounds := fixture(t)
	entries, _ := Build(u, nodes, inbounds, nil)
	links := ShareLinks(entries)
	if len(links) != 3 {
		t.Fatalf("got %d links, want 3", len(links))
	}

	byScheme := map[string]*url.URL{}
	for _, l := range links {
		parsed, err := url.Parse(l)
		if err != nil {
			t.Fatalf("link does not parse as a URL: %q (%v)", l, err)
		}
		byScheme[parsed.Scheme] = parsed
	}

	vless := byScheme["vless"]
	if vless == nil {
		t.Fatal("no vless link")
	}
	q := vless.Query()
	for _, k := range []string{"pbk", "sni", "sid", "flow", "type", "security"} {
		if q.Get(k) == "" {
			t.Errorf("vless link is missing %s: %s", k, vless)
		}
	}
	if q.Get("security") != "reality" || q.Get("flow") != service.FlowVision {
		t.Errorf("vless link has wrong security/flow: %s", vless)
	}
	if vless.User.Username() != u.VlessUUID {
		t.Errorf("vless uuid = %q, want %q", vless.User.Username(), u.VlessUUID)
	}

	anytls := byScheme["anytls"]
	if anytls == nil {
		t.Fatal("no anytls link")
	}
	// AnyTLS multiplexes on its own; advertising another muxer breaks it.
	for _, k := range []string{"mux", "smux", "multiplex"} {
		if anytls.Query().Get(k) != "" {
			t.Errorf("anytls link advertises %s: %s", k, anytls)
		}
	}

	ss := byScheme["ss"]
	if ss == nil {
		t.Fatal("no ss link")
	}
	raw, err := base64.RawURLEncoding.DecodeString(ss.User.Username())
	if err != nil {
		t.Fatalf("ss userinfo is not raw base64url: %q (%v)", ss.User.Username(), err)
	}
	if !strings.HasPrefix(string(raw), service.SSMethod+":") {
		t.Errorf("ss userinfo = %q, want it to start with the method", raw)
	}
}

// The fragment is what a client shows in its list. Spaces have to survive as
// spaces, not as the '+' that query escaping would produce.
func TestFragmentEscaping(t *testing.T) {
	if got := frag("tokyo node"); got != "tokyo%20node" {
		t.Errorf("frag = %q, want tokyo%%20node", got)
	}
}

func TestIPv6AddressIsBracketed(t *testing.T) {
	if got := hostPort("2001:db8::1", 443); got != "[2001:db8::1]:443" {
		t.Errorf("hostPort = %q", got)
	}
	if got := hostPort("1.2.3.4", 443); got != "1.2.3.4:443" {
		t.Errorf("hostPort = %q", got)
	}
}

func TestBase64Decodes(t *testing.T) {
	u, nodes, inbounds := fixture(t)
	entries, _ := Build(u, nodes, inbounds, nil)

	raw, err := base64.StdEncoding.DecodeString(Base64(entries, nil))
	if err != nil {
		t.Fatalf("not valid base64: %v", err)
	}
	if n := len(strings.Split(strings.TrimSpace(string(raw)), "\n")); n != 3 {
		t.Fatalf("decoded to %d lines, want 3", n)
	}
}

// The notice entries carry usage and expiry in their names, for the clients
// that put a server list in front of the user and the header nowhere.
func TestNoticesAreAppendedNotPrepended(t *testing.T) {
	u, nodes, inbounds := fixture(t)
	entries, _ := Build(u, nodes, inbounds, nil)

	// Stored the way the panel stores an expiry: the last second of a day in
	// the operator's own timezone. Formatting that in UTC would show the day
	// after for anyone east of Greenwich, and the panel's own list would then
	// disagree with the client.
	expires := time.Date(2026, 12, 31, 23, 59, 59, 0, time.Local)
	now := time.Date(2026, 12, 1, 23, 59, 59, 0, time.Local)
	notices := InfoLinks(3<<30, 10<<30, &expires, now)
	if len(notices) != 2 {
		t.Fatalf("got %d notices, want one for traffic and one for expiry", len(notices))
	}

	raw, err := base64.StdEncoding.DecodeString(Base64(entries, notices))
	if err != nil {
		t.Fatalf("not valid base64: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 5 {
		t.Fatalf("got %d lines, want 3 servers + 2 notices", len(lines))
	}
	// A client that makes the first entry current must land on a real server.
	if strings.Contains(lines[0], infoAddress) {
		t.Error("a notice is first in the list")
	}
	for _, want := range []string{"3.00 GiB", "10.00 GiB", "2026-12-31", "剩 30 天"} {
		if !strings.Contains(strings.Join(notices, "\n"), url.QueryEscape(want)) &&
			!strings.Contains(strings.Join(notices, "\n"), frag(want)) {
			t.Errorf("notices do not mention %q", want)
		}
	}
}

// No limit and no expiry means nothing worth saying, and two lines reading
// "unlimited" would just be noise in the server list.
func TestNoNoticesForAnUnlimitedUser(t *testing.T) {
	if n := InfoLinks(0, 0, nil, time.Now()); len(n) != 0 {
		t.Errorf("got %d notices for an unlimited user, want none", len(n))
	}
}

// ── sing-box ────────────────────────────────────────────────────────────────

func TestSingBoxConfigIsUsable(t *testing.T) {
	u, nodes, inbounds := fixture(t)
	entries, _ := Build(u, nodes, inbounds, nil)

	data, err := SingBox(entries)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}

	outs, _ := cfg["outbounds"].([]any)
	types := map[string]map[string]any{}
	for _, o := range outs {
		m := o.(map[string]any)
		types[m["tag"].(string)] = m
	}
	for _, tag := range []string{"vless-tokyo", "anytls-tokyo", "ss-tokyo",
		tagAuto, tagProxy, tagDirect} {
		if _, ok := types[tag]; !ok {
			t.Errorf("outbound %q missing", tag)
		}
	}

	// Reality is a hidden handshake; without uTLS the client's own TLS
	// fingerprint gives it away, which defeats the protocol.
	tls := types["vless-tokyo"]["tls"].(map[string]any)
	utls, ok := tls["utls"].(map[string]any)
	if !ok || utls["enabled"] != true {
		t.Error("vless outbound does not enable utls")
	}
	if reality, ok := tls["reality"].(map[string]any); !ok || reality["public_key"] == "" {
		t.Error("vless outbound is missing the reality public key")
	}
}

// A user with nothing assigned must still get a config sing-box will start. An
// empty selector makes it refuse to launch, which looks like a broken client
// rather than an expired account.
func TestSingBoxWithNoEntriesStillStarts(t *testing.T) {
	data, err := SingBox(nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var cfg struct {
		Outbounds []struct {
			Type      string   `json:"type"`
			Tag       string   `json:"tag"`
			Outbounds []string `json:"outbounds"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	for _, o := range cfg.Outbounds {
		if (o.Type == "selector" || o.Type == "urltest") && len(o.Outbounds) == 0 {
			t.Fatalf("%s %q has no members", o.Type, o.Tag)
		}
	}
}

// ── clash ───────────────────────────────────────────────────────────────────

func TestClashConfigIsUsable(t *testing.T) {
	u, nodes, inbounds := fixture(t)
	entries, _ := Build(u, nodes, inbounds, nil)

	data, err := Clash(entries)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var cfg struct {
		Proxies []map[string]any `yaml:"proxies"`
		Groups  []map[string]any `yaml:"proxy-groups"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("not valid YAML: %v", err)
	}
	if len(cfg.Proxies) != 3 {
		t.Fatalf("got %d proxies, want 3", len(cfg.Proxies))
	}

	byType := map[string]map[string]any{}
	for _, p := range cfg.Proxies {
		byType[p["type"].(string)] = p
	}
	if r, ok := byType["vless"]["reality-opts"].(map[string]any); !ok || r["public-key"] == "" {
		t.Error("vless proxy is missing reality-opts.public-key")
	}
	if byType["ss"]["cipher"] != service.SSMethod {
		t.Errorf("ss cipher = %v", byType["ss"]["cipher"])
	}
	if byType["anytls"]["password"] == "" {
		t.Error("anytls proxy has no password")
	}
}

// Every credential the panel emits is base64 or base64url, so it contains '+',
// '/' and '='. A round trip through a real YAML parser is what proves they
// survive.
func TestClashCredentialsSurviveYAML(t *testing.T) {
	u, nodes, inbounds := fixture(t)
	entries, _ := Build(u, nodes, inbounds, nil)

	data, err := Clash(entries)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var cfg struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("not valid YAML: %v", err)
	}

	want := map[string]string{}
	for _, e := range entries {
		if e.Password != "" {
			want[e.Name] = e.Password
		}
	}
	for _, p := range cfg.Proxies {
		name := p["name"].(string)
		if expected, ok := want[name]; ok {
			if got, _ := p["password"].(string); got != expected {
				t.Errorf("%s: password did not survive YAML: got %q want %q", name, got, expected)
			}
		}
	}
}

// ── dispatch ────────────────────────────────────────────────────────────────

func TestDetect(t *testing.T) {
	cases := []struct {
		name   string
		ua     string
		accept string
		query  string
		want   Format
	}{
		{"sing-box", "sing-box 1.14.0", "", "", FormatSingBox},
		{"SFA", "SFA/1.9.0 (Android)", "", "", FormatSingBox},
		{"hiddify", "HiddifyNext/2.0", "", "", FormatSingBox},
		{"mihomo", "mihomo/1.18", "", "", FormatClash},
		{"clash-verge", "clash-verge/1.5", "", "", FormatClash},
		{"stash", "Stash/2.5", "", "", FormatClash},
		{"v2rayNG", "v2rayNG/1.8.5", "", "", FormatBase64},
		{"browser", "Mozilla/5.0", "text/html,application/xhtml+xml", "", FormatHTML},
		{"real browser accept", "Mozilla/5.0",
			"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", "", FormatHTML},
		{"curl", "curl/8.0", "*/*", "", FormatBase64},

		// An unrecognised client that sends a catch-all Accept must still get a
		// configuration. Handing it the web page gives it something it cannot
		// parse and cannot report usefully.
		{"unknown client, catch-all accept", "SomeClient/1.0", "text/html,*/*", "", FormatBase64},
		{"unknown client, wildcard first", "SomeClient/1.0", "*/*,text/html", "", FormatBase64},
		{"explicit override", "sing-box", "", "clash", FormatClash},
		{"explicit html", "curl/8.0", "", "html", FormatHTML},
	}
	for _, c := range cases {
		target := "/sub/tok"
		if c.query != "" {
			target += "?format=" + c.query
		}
		r := httptest.NewRequest(http.MethodGet, target, nil)
		if c.ua != "" {
			r.Header.Set("User-Agent", c.ua)
		}
		if c.accept != "" {
			r.Header.Set("Accept", c.accept)
		}
		if got := Detect(r); got != c.want {
			t.Errorf("%s: Detect = %q, want %q", c.name, got, c.want)
		}
	}
}

// A client that sends both a recognised User-Agent and an HTML Accept header
// must get its own format. Browsers are identified by Accept precisely because
// so many clients also claim to be Mozilla.
func TestUserAgentBeatsAcceptHTML(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/sub/tok", nil)
	r.Header.Set("User-Agent", "ClashMetaForAndroid/2.9 Mozilla/5.0")
	r.Header.Set("Accept", "text/html,*/*")
	if got := Detect(r); got != FormatClash {
		t.Errorf("Detect = %q, want clash", got)
	}
}

func TestUserInfoHeader(t *testing.T) {
	got := UserInfoHeader(1024, 2048, 0)
	if got != "upload=0; download=1024; total=2048" {
		t.Errorf("header = %q", got)
	}
	if got := UserInfoHeader(1, 2, 1700000000); !strings.HasSuffix(got, "; expire=1700000000") {
		t.Errorf("expiry missing: %q", got)
	}
}

// The DNS block has bitten this package once already: sing-box 1.14 removed the
// pre-1.12 form, where a server was one "address" string like
// "https://1.1.1.1/dns-query", and rejects a config carrying it outright — so
// every client got a subscription it could not load.
//
// The licence boundary means this package cannot import sing-box to validate
// against the real schema, so the shape is pinned here instead.
func TestSingBoxDNSUsesTheCurrentServerFormat(t *testing.T) {
	u, nodes, inbounds := fixture(t)
	entries, _ := Build(u, nodes, inbounds, nil)

	data, err := SingBox(entries)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var cfg struct {
		DNS struct {
			Servers []map[string]any `json:"servers"`
			Rules   []map[string]any `json:"rules"`
			Final   string           `json:"final"`
		} `json:"dns"`
		Route struct {
			DefaultDomainResolver map[string]any `json:"default_domain_resolver"`
		} `json:"route"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if len(cfg.DNS.Servers) == 0 {
		t.Fatal("no DNS servers")
	}
	for _, s := range cfg.DNS.Servers {
		if _, legacy := s["address"]; legacy {
			t.Errorf("DNS server %v uses the removed \"address\" form", s["tag"])
		}
		if s["type"] == nil || s["type"] == "" {
			t.Errorf("DNS server %v has no type", s["tag"])
		}
		if s["server"] == nil || s["server"] == "" {
			t.Errorf("DNS server %v has no server", s["tag"])
		}
	}
	if cfg.DNS.Final == "" {
		t.Error("dns.final is unset, so the fallback is whatever comes first")
	}
	for _, s := range cfg.DNS.Servers {
		if s["detour"] == tagDirect {
			t.Errorf("DNS server %v detours to the empty direct outbound, "+
				"which sing-box rejects as meaningless", s["tag"])
		}
	}
	// Also removed in 1.14: sing-box will not start without being told which
	// resolver turns a dialled name into an address.
	if cfg.Route.DefaultDomainResolver["server"] == nil {
		t.Error("route.default_domain_resolver is unset; sing-box 1.14 refuses to start")
	}

	// A node reached by name cannot have that name resolved through the proxy
	// it is the far end of.
	var got []string
	for _, r := range cfg.DNS.Rules {
		if r["server"] != "local" {
			continue
		}
		for _, d := range r["domain"].([]any) {
			got = append(got, d.(string))
		}
	}
	want := serverDomains(entries)
	if len(want) == 0 {
		t.Fatal("fixture has no node addressed by name, so the loop is untested")
	}
	for _, host := range want {
		if !slices.Contains(got, host) {
			t.Errorf("node address %q is not pinned to the local resolver", host)
		}
	}
}
