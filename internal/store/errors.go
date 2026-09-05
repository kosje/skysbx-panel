package store

import (
	"errors"
	"strings"
)

// ErrConflict reports a uniqueness violation — a duplicate user name, node name
// or inbound tag. Without it every such collision surfaces as an opaque
// "something went wrong", when it is really something the operator can fix by
// typing a different name.
var ErrConflict = errors.New("already exists")

// asConflict converts a driver-level uniqueness error into ErrConflict and
// leaves everything else alone.
//
// It matches on the message rather than a driver error code: modernc.org/sqlite
// does not export a typed error whose code can be compared, and the text is
// SQLite's own and stable across the driver's releases.
func asConflict(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "PRIMARY KEY constraint failed") {
		return errors.Join(ErrConflict, err)
	}
	return err
}
