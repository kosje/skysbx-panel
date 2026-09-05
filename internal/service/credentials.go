package service

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// randomBytes panics rather than returning an error. crypto/rand does not fail
// in practice, and every caller here is minting a credential — continuing with
// a predictable one would be worse than crashing at startup.
func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("skysb: cannot read random bytes: " + err.Error())
	}
	return b
}

// NewUUID returns a random RFC 4122 version 4 UUID, the VLESS credential.
func NewUUID() string {
	b := randomBytes(16)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]), hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]))
}

// NewPassword returns the AnyTLS credential: an opaque URL-safe string that
// travels verbatim in both the sing-box user list and the anytls:// link.
func NewPassword() string {
	return base64.RawURLEncoding.EncodeToString(randomBytes(24))
}

// NewSSPassword returns a Shadowsocks 2022 key: base64 of exactly 32 bytes,
// which is what 2022-blake3-aes-256-gcm requires.
//
// Panels that have to interoperate with Xray generate an ASCII secret and
// base64 it a second time on the way out. Owning both ends means the stored
// value can be the wire value: this string goes straight into sing-box's user
// list and straight into the ss:// credential, with no re-encoding step to get
// wrong.
func NewSSPassword() string {
	return base64.StdEncoding.EncodeToString(randomBytes(32))
}

// NewSubToken returns the opaque path segment of a subscription URL. It is a
// bearer credential — anyone holding it gets the user's configs — so it is as
// long as a session token, not a short id.
func NewSubToken() string {
	return base64.RawURLEncoding.EncodeToString(randomBytes(18))
}

// NewNodeToken returns the secret a node presents when it dials the panel.
// Shown once at creation; only its hash is stored.
func NewNodeToken() string {
	return base64.RawURLEncoding.EncodeToString(randomBytes(32))
}

// NewShortID returns a Reality short id: an even number of hex digits, at most
// 16. Eight is what every client defaults to and is plenty for one node.
func NewShortID() string {
	return hex.EncodeToString(randomBytes(4))
}
