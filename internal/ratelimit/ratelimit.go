package ratelimit

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// Package ratelimit is a token bucket per client address, for the handlers that verify a credential.
//
// Those handlers are the expensive ones and the unauthenticated ones at the same
// time: checking a password means bcrypt, which is deliberately slow, and
// anybody can ask for it. Without a limit the cost of an attempt is entirely the
// panel's, and a single client can spend all of it.
//
// It also puts a ceiling on guessing. The administrator password is the one
// credential in this system that a human chose, so it is the only one worth
// trying to guess at all.
//
// Deliberately in-memory and per-process. The panel is a single binary; a
// shared store would add a dependency to defend against an attacker who already
// has to get through TLS first.

type bucket struct {
	tokens float64
	last   time.Time
}

type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket

	burst  float64       // attempts allowed back to back
	refill float64       // tokens per second
	ttl    time.Duration // idle time after which a bucket is forgotten
	sweep  time.Time
}

func New(burst float64, per time.Duration) *Limiter {
	return &Limiter{
		buckets: map[string]*bucket{},
		burst:   burst,
		refill:  1 / per.Seconds(),
		// Long enough that a bucket survives the gap between two attempts by
		// the same client, short enough that a spray across many addresses does
		// not hold memory for them all.
		ttl: 10 * time.Minute,
	}
}

// Allow reports whether this address may make an attempt now, and spends a
// token if so.
func (l *Limiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Sweeping here rather than on a goroutine: the map only grows when it is
	// being used, so the moment it is being used is the right time to prune it.
	if now.Sub(l.sweep) > l.ttl {
		for k, b := range l.buckets {
			if now.Sub(b.last) > l.ttl {
				delete(l.buckets, k)
			}
		}
		l.sweep = now
	}

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * l.refill
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// ClientIP is the bucket key. The port has to go: a new source port per
// connection would give every attempt its own bucket and limit nothing.
//
// Forwarded headers are not consulted. The panel terminates TLS itself, so
// RemoteAddr is the real peer; trusting a header instead would let the client
// pick its own bucket, which is worse than having no limit at all because it
// looks like one.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
