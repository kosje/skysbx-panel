package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kosje/skysbx-panel/internal/service"
	"github.com/kosje/skysbx-panel/internal/store"
)

// A template error only happens at render time, and nothing else in the suite
// renders the two blocks the relay feature added: the select on the edit form,
// and the list of listeners a node runs for other nodes.
func TestRelayUIRenders(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	svc := service.New(st)
	origin, _, err := svc.CreateNode("tokyo", "jp.example.com", "JP")
	if err != nil {
		t.Fatal(err)
	}
	relay, _, err := svc.CreateNode("hongkong", "hk.example.com", "HK")
	if err != nil {
		t.Fatal(err)
	}
	in, err := svc.CreateInbound(origin.ID, service.InboundSpec{
		Protocol: store.ProtoVLESS, Port: 8443,
		RelayNodeID: relay.ID, RelayPort: 443,
	})
	if err != nil {
		t.Fatal(err)
	}

	ch := &fakeChannel{connected: true, known: true,
		tags: map[string]bool{in.Tag: true, service.RelayTag(in.Tag): true}}
	srv, err := New(svc, ch, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}

	render := func(nodeID, editID int64) string {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/nodes/1/inbounds", nil)
		srv.renderInboundsFull(w, r, nodeID, http.StatusOK, editID)
		if w.Code != http.StatusOK {
			t.Fatalf("render node %d edit %d = %d: %s", nodeID, editID, w.Code, w.Body)
		}
		return w.Body.String()
	}

	// The origin's list: the 连接地址 column has to name the relay node, not the
	// origin's own address — that column is where an operator checks what is
	// actually being handed out.
	body := render(origin.ID, 0)
	for _, want := range []string{"站内中转", "hongkong:443"} {
		if !strings.Contains(body, want) {
			t.Errorf("the origin's inbound list is missing %q", want)
		}
	}

	// The edit form: the select, its current value, and the port field.
	body = render(origin.ID, in.ID)
	for _, want := range []string{
		`name="relay_node_id"`, `name="relay_port"`, "经由 hongkong", "selected",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the edit form is missing %q", want)
		}
	}
	// A node must not be offered as a relay for its own inbound.
	if strings.Contains(body, "经由 tokyo") {
		t.Error("the origin node was offered as a relay for its own inbound")
	}

	// The relay node's page. Its own inbound list is empty, and without this
	// section the port it is holding appears nowhere at all.
	body = render(relay.ID, 0)
	for _, want := range []string{
		"本节点为其它节点中转", service.RelayTag(in.Tag), "jp.example.com:8443", "转发中",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the relay node's page is missing %q", want)
		}
	}

	// A node carrying nothing must not grow an empty section.
	if err := svc.DeleteInbound(in.ID); err != nil {
		t.Fatal(err)
	}
	if body := render(relay.ID, 0); strings.Contains(body, "本节点为其它节点中转") {
		t.Error("the section is shown on a node that carries no relays")
	}
}

// With one node there is nowhere to relay to, and hiding the control there left
// the help text describing a field that was not on the page. The control stays,
// disabled, and says what is missing.
func TestRelaySelectExplainsItselfOnASingleNodePanel(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	svc := service.New(st)
	node, _, err := svc.CreateNode("tokyo", "jp.example.com", "JP")
	if err != nil {
		t.Fatal(err)
	}
	in, err := svc.CreateInbound(node.ID, service.InboundSpec{
		Protocol: store.ProtoShadowsocks, Port: 8388,
	})
	if err != nil {
		t.Fatal(err)
	}

	srv, err := New(svc, &fakeChannel{connected: true, known: true,
		tags: map[string]bool{in.Tag: true}},
		slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/nodes/1/inbounds", nil)
	srv.renderInboundsFull(w, r, node.ID, http.StatusOK, 0)
	body := w.Body.String()

	for _, want := range []string{"站内中转", `name="relay_node_id"`,
		"disabled", "需要至少两个节点", "只有这一个节点"} {
		if !strings.Contains(body, want) {
			t.Errorf("the create form is missing %q", want)
		}
	}

	// The edit form too, and it must not offer this node as its own relay.
	w = httptest.NewRecorder()
	srv.renderInboundsFull(w, r, node.ID, http.StatusOK, in.ID)
	body = w.Body.String()
	if !strings.Contains(body, "需要至少两个节点") {
		t.Error("the edit form hides the relay select instead of explaining it")
	}
	if strings.Contains(body, "经由 tokyo") {
		t.Error("the node was offered as a relay for its own inbound")
	}
}
