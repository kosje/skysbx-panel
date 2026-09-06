package web

import (
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kosje/skysbx-panel/internal/service"
	"github.com/kosje/skysbx-panel/internal/store"
)

func hardenedServer(t *testing.T) http.Handler {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	svc := service.New(st)
	if err := svc.SetAdmin("admin", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	srv, err := New(svc, &fakeChannel{}, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	if err != nil {
		t.Fatal(err)
	}
	return srv.Handler()
}

// Every destructive action in this UI is one button, and the administrator is
// logged in with a Lax cookie that a top-level frame load carries. Refusing to
// be framed is what stands between that and a click the operator never meant.
func TestAdminUIRefusesToBeFramed(t *testing.T) {
	h := hardenedServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))

	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("no frame-ancestors in the policy: %q", csp)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	// The subscription token travels in a URL path, so it would otherwise be in
	// the Referer of anything followed from that page.
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q", got)
	}
	// The policy has to actually permit the one script the page loads, or the
	// UI is hardened into uselessness.
	if !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("the policy blocks the panel's own script: %q", csp)
	}
}

// A subscription response is a bearer credential in a text file. Nothing
// between the panel and the client may keep a copy.
func TestSubscriptionIsNotCacheable(t *testing.T) {
	h := hardenedServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sub/whatever", nil))

	cc := rec.Header().Get("Cache-Control")
	if !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store — even a 404 answers on this path", cc)
	}
}

// Only over TLS: sending it on plain HTTP is meaningless, and a browser that
// received it there would be trusting an unauthenticated hop.
func TestHSTSOnlyOverTLS(t *testing.T) {
	h := hardenedServer(t)

	plain := httptest.NewRecorder()
	h.ServeHTTP(plain, httptest.NewRequest(http.MethodGet, "/login", nil))
	if got := plain.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS sent over plain HTTP: %q", got)
	}

	secure := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/login", nil)
	r.TLS = &tls.ConnectionState{}
	h.ServeHTTP(secure, r)
	if got := secure.Header().Get("Strict-Transport-Security"); got == "" {
		t.Error("no HSTS over TLS")
	}
}

// bcrypt is deliberately slow, so an unthrottled login is both a guessing
// oracle and a way to spend the panel's CPU without holding any credential.
func TestLoginIsRateLimited(t *testing.T) {
	h := hardenedServer(t)

	post := func() int {
		form := url.Values{"username": {"admin"}, "password": {"wrong"}}
		r := httptest.NewRequest(http.MethodPost, "/login",
			strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.RemoteAddr = "203.0.113.9:44312"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}

	limited := false
	for i := range 20 {
		if code := post(); code == http.StatusTooManyRequests {
			limited = true
			if i < 5 {
				t.Errorf("throttled after only %d attempts; a mistyped password "+
					"should not lock the operator out", i+1)
			}
			break
		}
	}
	if !limited {
		t.Error("twenty wrong passwords in a row were all answered")
	}

	// A different address is unaffected.
	form := url.Values{"username": {"admin"}, "password": {"wrong"}}
	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "198.51.100.7:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code == http.StatusTooManyRequests {
		t.Error("one address being throttled locked out another")
	}
}

// The reset-day select has to render on both the create form and the edit row,
// with the user's current day preselected — a template that silently rendered
// "不重置" would turn every save into an unnoticed cancellation of the schedule.
func TestResetDaySelectRenders(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	svc := service.New(st)
	if err := svc.SetAdmin("admin", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	u, err := svc.CreateUser(service.NewUser{Name: "alice", ResetDay: 15,
		TrafficLimit: 50 << 30})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(svc, &fakeChannel{}, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	if err != nil {
		t.Fatal(err)
	}

	// The create form: every option present, "不重置" preselected.
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/users", nil)
	srv.renderUsers(rec, r, http.StatusOK)
	body := rec.Body.String()
	for _, want := range []string{
		`name="reset_day"`, `value="created"`, "按创建日",
		`value="15"`, "每月最后一天", "每月 29 号（短月顺延到月底）",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the users page is missing %q", want)
		}
	}
	// And the list shows when the counter next goes to zero, beside the limit.
	if !strings.Contains(body, "重置") {
		t.Error("the list does not say when the allowance resets")
	}

	// The edit row: the user's own day is the selected option.
	rec = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/users/1/edit", nil)
	r.SetPathValue("id", "1")
	srv.editUser(rec, r)
	body = rec.Body.String()
	if !strings.Contains(body, `<option value="15" selected>`) {
		t.Errorf("the edit form does not preselect day %d: %s", u.ResetDay, body)
	}
}
