package service

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/kosje/skysbx-panel/internal/store"
)

const (
	settingAdminUser = "admin.username"
	settingAdminHash = "admin.password_hash"
)

var (
	ErrBadCredentials = errors.New("bad credentials")

	// ErrTooManyAttempts is distinct from bad credentials on purpose: the
	// caller answers 429 rather than 401, so a node that is merely being
	// throttled backs off instead of concluding its token is wrong.
	ErrTooManyAttempts = errors.New("too many attempts")
)

// HashSecret hashes a password or a node token. bcrypt caps its input at 72
// bytes and silently truncates beyond that; every secret this panel generates
// is well under, and admin passwords are length-checked before they get here.
func HashSecret(secret string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash secret: %w", err)
	}
	return string(h), nil
}

func CheckSecret(hash, secret string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(secret)) == nil
}

// SetAdmin stores the single administrator's credentials.
func (s *Service) SetAdmin(username, password string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return invalid("username is required")
	}
	if len(password) < 12 {
		return invalid("password must be at least 12 characters")
	}
	if len(password) > 72 {
		// bcrypt would truncate silently, so anything past its limit would not
		// actually be checked. Refuse rather than pretend.
		return invalid("password must be at most 72 characters")
	}
	hash, err := HashSecret(password)
	if err != nil {
		return err
	}
	if err := s.st.SetSetting(settingAdminUser, username); err != nil {
		return err
	}
	return s.st.SetSetting(settingAdminHash, hash)
}

// AdminExists reports whether the first-run setup has happened.
func (s *Service) AdminExists() (bool, error) {
	h, err := s.st.Setting(settingAdminHash)
	return h != "", err
}

func (s *Service) AdminUsername() (string, error) {
	return s.st.Setting(settingAdminUser)
}

// CheckAdmin verifies a login. It compares the password even when the username
// is wrong, so a wrong username and a wrong password take the same time and
// cannot be told apart by an observer.
func (s *Service) CheckAdmin(username, password string) error {
	wantUser, err := s.st.Setting(settingAdminUser)
	if err != nil {
		return err
	}
	hash, err := s.st.Setting(settingAdminHash)
	if err != nil {
		return err
	}
	if hash == "" {
		return ErrBadCredentials
	}

	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(wantUser)) == 1
	passOK := CheckSecret(hash, password)
	if !userOK || !passOK {
		return ErrBadCredentials
	}
	return nil
}

// TokenSHA is the lookup key for a join token: hex SHA-256 of it.
//
// A plain hash rather than bcrypt, and that is the right primitive here. bcrypt
// exists to make guessing a low-entropy secret expensive; a join token is 32
// bytes from crypto/rand, so there is nothing to guess and the cost falls
// entirely on the panel. It also cannot be indexed, which forced authentication
// to be a linear scan — one bcrypt per node, on an endpoint that by definition
// has not authenticated anyone yet.
func TokenSHA(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// AuthenticateNode finds the node whose join token matches.
//
// The fast path is a single indexed lookup, so a token no node holds costs one
// index probe. The scan below it exists only for nodes whose token was minted
// before token_sha existed — their plaintext is gone, so the column cannot be
// backfilled and only that node presenting its token can fill it in. Each such
// node upgrades itself on its next handshake, and once none are left the scan
// is unreachable.
//
// allowSlow is consulted immediately before the scan and nowhere else, so the
// caller throttles exactly the requests that are about to be expensive and lets
// every other one through untouched. A nil value means no throttling.
//
// It is a callback rather than a bool because the answer has to be asked for at
// the moment the cost would be paid: deciding beforehand would either spend a
// token on requests that never reach the scan, or check after the bcrypt has
// already run, which throttles nothing.
func (s *Service) AuthenticateNode(token string, allowSlow func() bool) (int64, error) {
	if token == "" {
		return 0, ErrBadCredentials
	}

	sha := TokenSHA(token)
	n, err := s.st.NodeByTokenSHA(sha)
	switch {
	case err == nil:
		if !n.Enabled {
			return 0, ErrBadCredentials
		}
		return n.ID, nil
	case !errors.Is(err, store.ErrNotFound):
		return 0, err
	}

	// Nothing left to try unless some node predates the column. Asking first
	// keeps the ordinary wrong-token case at one indexed query and no bcrypt,
	// which is the entire point.
	missing, err := s.st.NodesMissingTokenSHA()
	if err != nil {
		return 0, err
	}
	if missing == 0 {
		return 0, ErrBadCredentials
	}
	if allowSlow != nil && !allowSlow() {
		return 0, ErrTooManyAttempts
	}

	nodes, err := s.st.Nodes()
	if err != nil {
		return 0, err
	}
	for _, n := range nodes {
		if !n.Enabled || n.TokenSHA != "" {
			continue
		}
		if CheckSecret(n.TokenHash, token) {
			// Upgrade it, so this node never takes the slow path again — and
			// so the scan disappears entirely once every node has connected.
			// A failure here is not worth refusing an otherwise good token
			// over: the node stays on the slow path and tries again next time.
			_ = s.st.SetNodeTokenSHA(n.ID, sha)
			return n.ID, nil
		}
	}
	return 0, ErrBadCredentials
}
