package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBurstThenRefill(t *testing.T) {
	l := New(3, time.Second)
	now := time.Now()

	for i := range 3 {
		if !l.Allow("1.2.3.4", now) {
			t.Fatalf("attempt %d refused inside the burst", i+1)
		}
	}
	if l.Allow("1.2.3.4", now) {
		t.Fatal("a fourth attempt was allowed with no time passing")
	}

	// One token a second.
	if l.Allow("1.2.3.4", now.Add(900*time.Millisecond)) {
		t.Error("allowed before a token had refilled")
	}
	if !l.Allow("1.2.3.4", now.Add(time.Second)) {
		t.Error("still refused after a full second")
	}

	// Refill is capped at the burst, so a long quiet period does not bank
	// unlimited attempts.
	later := now.Add(time.Hour)
	for i := range 3 {
		if !l.Allow("1.2.3.4", later) {
			t.Fatalf("attempt %d refused after a long idle period", i+1)
		}
	}
	if l.Allow("1.2.3.4", later) {
		t.Error("an hour of idling banked more than the burst")
	}
}

// Buckets are per address. One client exhausting its own must not lock out
// everyone else — which is what a single global counter would do, and would
// make the limiter itself the denial of service.
func TestBucketsAreIndependent(t *testing.T) {
	l := New(1, time.Minute)
	now := time.Now()

	if !l.Allow("1.1.1.1", now) || l.Allow("1.1.1.1", now) {
		t.Fatal("first bucket did not behave")
	}
	if !l.Allow("2.2.2.2", now) {
		t.Error("one address exhausting its bucket locked out another")
	}
}

// The port has to be dropped. A new source port per connection would give every
// attempt its own bucket, which limits nothing at all.
func TestClientIPIgnoresThePort(t *testing.T) {
	for _, tc := range []struct{ remote, want string }{
		{"203.0.113.9:44312", "203.0.113.9"},
		{"203.0.113.9:1", "203.0.113.9"},
		{"[2001:db8::1]:44312", "2001:db8::1"},
		{"garbage", "garbage"}, // no panic, and still a stable key
	} {
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		r.RemoteAddr = tc.remote
		if got := ClientIP(r); got != tc.want {
			t.Errorf("ClientIP(%q) = %q, want %q", tc.remote, got, tc.want)
		}
	}
}

// Forwarded headers must not be consulted: the client sets them, so honouring
// one would let an attacker pick a fresh bucket per request. That is worse than
// having no limiter, because it looks like there is one.
func TestForwardedHeadersDoNotChooseTheBucket(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r.RemoteAddr = "203.0.113.9:44312"
	r.Header.Set("X-Forwarded-For", "10.0.0.1")
	r.Header.Set("X-Real-IP", "10.0.0.2")
	if got := ClientIP(r); got != "203.0.113.9" {
		t.Errorf("ClientIP followed a client-supplied header: %q", got)
	}
}

// The map must not grow forever under a spray across many addresses.
func TestIdleBucketsAreSwept(t *testing.T) {
	l := New(1, time.Minute)
	now := time.Now()
	for i := range 500 {
		l.Allow(string(rune(i)), now)
	}
	if len(l.buckets) != 500 {
		t.Fatalf("expected 500 buckets, got %d", len(l.buckets))
	}
	// Well past the ttl, one more call sweeps the rest.
	l.Allow("survivor", now.Add(time.Hour))
	if len(l.buckets) != 1 {
		t.Errorf("%d buckets survived the sweep, want just the live one", len(l.buckets))
	}
}
