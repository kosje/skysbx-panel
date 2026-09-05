package service

import (
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"testing"
)

var uuidRE = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewUUIDIsVersion4(t *testing.T) {
	for i := 0; i < 100; i++ {
		u := NewUUID()
		if !uuidRE.MatchString(u) {
			t.Fatalf("not a v4 UUID: %q", u)
		}
	}
}

// The whole point of the SS2022 key is its decoded length: 2022-blake3-aes-256-gcm
// rejects anything that is not exactly 32 bytes, and the failure mode is a
// runtime "bad key" on the node rather than anything visible in the panel.
func TestNewSSPasswordDecodesTo32Bytes(t *testing.T) {
	for i := 0; i < 100; i++ {
		p := NewSSPassword()
		raw, err := base64.StdEncoding.DecodeString(p)
		if err != nil {
			t.Fatalf("not standard base64: %q (%v)", p, err)
		}
		if len(raw) != 32 {
			t.Fatalf("decoded to %d bytes, want 32: %q", len(raw), p)
		}
	}
}

// Reality short ids must be hex of even length, at most 16 characters.
func TestNewShortIDIsHex(t *testing.T) {
	for i := 0; i < 100; i++ {
		s := NewShortID()
		if len(s)%2 != 0 || len(s) > 16 {
			t.Fatalf("bad length %d: %q", len(s), s)
		}
		if _, err := hex.DecodeString(s); err != nil {
			t.Fatalf("not hex: %q", s)
		}
	}
}

// Credentials that end up in a URL must not need escaping, or they will be
// mangled by whichever client parses the share link least carefully.
func TestURLSafeCredentials(t *testing.T) {
	unsafe := regexp.MustCompile(`[^A-Za-z0-9_-]`)
	for _, c := range []struct {
		name string
		gen  func() string
	}{
		{"NewPassword", NewPassword},
		{"NewSubToken", NewSubToken},
		{"NewNodeToken", NewNodeToken},
	} {
		for i := 0; i < 50; i++ {
			v := c.gen()
			if unsafe.MatchString(v) {
				t.Fatalf("%s produced a value needing URL escaping: %q", c.name, v)
			}
		}
	}
}

func TestCredentialsAreUnique(t *testing.T) {
	for _, c := range []struct {
		name string
		gen  func() string
	}{
		{"NewUUID", NewUUID},
		{"NewPassword", NewPassword},
		{"NewSSPassword", NewSSPassword},
		{"NewSubToken", NewSubToken},
		{"NewNodeToken", NewNodeToken},
		{"NewShortID", NewShortID},
	} {
		seen := make(map[string]bool, 1000)
		for i := 0; i < 1000; i++ {
			v := c.gen()
			if seen[v] {
				t.Fatalf("%s repeated a value within 1000 draws: %q", c.name, v)
			}
			seen[v] = true
		}
	}
}
