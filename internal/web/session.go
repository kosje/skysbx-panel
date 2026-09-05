package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	sessionCookie = "skysb_session"
	sessionMaxAge = 12 * time.Hour
)

// Sessions are a signed cookie rather than a server-side table: there is one
// administrator, so there is nothing to look up. The cookie carries the
// username and an expiry, HMAC'd with a key generated on first run.
//
// A cookie — rather than a token in local storage — is also what keeps the UI
// swappable: the same scheme works unchanged if these handlers are ever
// replaced by a JSON API and a single-page frontend.
type sessions struct {
	key []byte
}

func newSessions(key []byte) *sessions { return &sessions{key: key} }

func newSessionKey() []byte {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic("skysb: cannot read random bytes: " + err.Error())
	}
	return k
}

func (s *sessions) sign(payload string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (s *sessions) issue(w http.ResponseWriter, username string, secure bool) {
	exp := time.Now().Add(sessionMaxAge)
	payload := username + "|" + strconv.FormatInt(exp.Unix(), 10)
	value := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + s.sign(payload)

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		Expires:  exp,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *sessions) clear(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

// user returns the signed-in username, or an error describing why not.
func (s *sessions) user(r *http.Request) (string, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", fmt.Errorf("no session cookie")
	}
	encoded, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return "", fmt.Errorf("malformed session cookie")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("malformed session payload")
	}
	payload := string(raw)

	// Compare with hmac.Equal, not ==: a byte-at-a-time comparison would leak
	// how much of a forged signature was correct.
	if !hmac.Equal([]byte(s.sign(payload)), []byte(sig)) {
		return "", fmt.Errorf("bad session signature")
	}

	username, expStr, ok := strings.Cut(payload, "|")
	if !ok {
		return "", fmt.Errorf("malformed session payload")
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return "", fmt.Errorf("malformed session expiry")
	}
	if time.Now().Unix() > exp {
		return "", fmt.Errorf("session expired")
	}
	return username, nil
}
