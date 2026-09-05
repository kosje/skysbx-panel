package service

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const (
	settingAdminUser = "admin.username"
	settingAdminHash = "admin.password_hash"
)

var ErrBadCredentials = errors.New("bad credentials")

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

// AuthenticateNode finds the node whose join token matches.
//
// Tokens are stored as bcrypt hashes, so there is no way to look one up by
// value; every enabled node has to be tried. That is fine at this scale, and
// the alternative — storing something reversible or indexable — would mean a
// database read discloses every node's credential.
func (s *Service) AuthenticateNode(token string) (int64, error) {
	if token == "" {
		return 0, ErrBadCredentials
	}
	nodes, err := s.st.Nodes()
	if err != nil {
		return 0, err
	}
	for _, n := range nodes {
		if !n.Enabled {
			continue
		}
		if CheckSecret(n.TokenHash, token) {
			return n.ID, nil
		}
	}
	return 0, ErrBadCredentials
}
