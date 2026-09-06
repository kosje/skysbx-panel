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

// fakeChannel stands in for the hub. Its whole job here is to answer the two
// questions the inbound page asks about what a node is really running.
type fakeChannel struct {
	connected bool
	tags      map[string]bool
	known     bool
	applyErr  string
}

func (f *fakeChannel) Handler() http.HandlerFunc    { return func(http.ResponseWriter, *http.Request) {} }
func (f *fakeChannel) Connected(int64) bool         { return f.connected }
func (f *fakeChannel) OnlineUsers() map[string]bool { return nil }
func (f *fakeChannel) UserIPCounts() map[string]int { return nil }
func (f *fakeChannel) ApplyError(int64) string      { return f.applyErr }
func (f *fakeChannel) LiveInbounds(int64) (map[string]bool, bool) {
	return f.tags, f.known
}

// An inbound the node refused must not sit in the list looking enabled. The
// panel's own database only records what the operator asked for, so the node's
// report is the only thing that can tell the two apart.
func TestInboundPageShowsWhatTheNodeIsActuallyServing(t *testing.T) {
	cases := []struct {
		name    string
		channel *fakeChannel
		want    string
		notWant string
		wantErr string
	}{{
		name:    "node has reported and is serving it",
		channel: &fakeChannel{connected: true, known: true, tags: map[string]bool{"ss-tokyo": true}},
		want:    "已生效",
		notWant: "未生效",
	}, {
		name: "node rejected the config and runs the previous one",
		channel: &fakeChannel{connected: true, known: true, tags: map[string]bool{},
			applyErr: "listen tcp 0.0.0.0:443: bind: address already in use"},
		want:    "未生效",
		wantErr: "address already in use",
	}, {
		// The dangerous case: silence must not read as failure, or every
		// restart would light the page up red for no reason.
		name:    "node has not reported",
		channel: &fakeChannel{},
		want:    "节点离线",
		notWant: "未生效",
	}, {
		// An inbound reaches the node asynchronously, so this is the state
		// every newly created one is in for a second or two. Showing 未生效
		// there trained the operator to ignore it.
		name:    "node is connected but has not caught up yet",
		channel: &fakeChannel{connected: true, known: true, tags: map[string]bool{}},
		want:    "确认中",
		notWant: "未生效",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := inboundPage(t, tc.channel)
			if !strings.Contains(body, tc.want) {
				t.Errorf("page does not say %q", tc.want)
			}
			if tc.notWant != "" && strings.Contains(body, tc.notWant) {
				t.Errorf("page says %q and should not", tc.notWant)
			}
			if tc.wantErr != "" && !strings.Contains(body, tc.wantErr) {
				t.Errorf("page does not show the reason %q", tc.wantErr)
			}
		})
	}
}

func inboundPage(t *testing.T, ch NodeChannel) string {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	svc := service.New(st)
	node, _, err := svc.CreateNode("tokyo", "jp.example.com", "JP")
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	if _, err := svc.CreateInbound(node.ID, service.InboundSpec{
		Protocol: store.ProtoShadowsocks, Port: 8388,
	}); err != nil {
		t.Fatalf("create inbound: %v", err)
	}

	srv, err := New(svc, ch, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/nodes/1/inbounds", nil)
	r.SetPathValue("id", "1")
	srv.renderInbounds(w, r, node.ID, http.StatusOK)
	return w.Body.String()
}

// A rejected configuration leaves the node running the previous one, and the
// node reports which tags it serves — not which settings. The tags are usually
// unchanged, so every row read 已生效 while the port beside it was not the port
// being listened on. Verified against a real node: an inbound moved to an
// occupied port kept serving its old one and the row still claimed 已生效.
func TestRejectedConfigDoesNotClaimRowsAreLive(t *testing.T) {
	// The node is serving the tag, and has also reported why it did not adopt
	// what it was last sent. Both are true at once, and that is the whole case.
	body := inboundPage(t, &fakeChannel{
		connected: true, known: true,
		tags:     map[string]bool{"ss-tokyo": true},
		applyErr: "listen tcp 0.0.0.0:18080: bind: address already in use",
	})
	if strings.Contains(body, "已生效") {
		t.Error("a row claimed 已生效 while the node is running an older configuration")
	}
	if !strings.Contains(body, "跑的是旧配置") {
		t.Error("the row does not say the node is running something else")
	}
	// The reason still has to be on the page; the pill replaces the claim, not
	// the explanation.
	if !strings.Contains(body, "address already in use") {
		t.Error("the node's reason vanished")
	}
}
