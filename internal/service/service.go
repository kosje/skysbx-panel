// Package service holds the panel's business logic.
//
// Nothing here knows about HTTP. That is deliberate: the htmx handlers are a
// thin transport on top of these calls, so a later JSON API — for a React UI,
// say — can sit beside them without any of the data model, node protocol,
// subscription or accounting logic moving.
package service

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/kosje/skysbx-panel/internal/store"
)

// Notifier lets the service tell the node hub that something it pushes has
// changed. Split in two because the two have very different costs: a user
// change is hot-swapped on the node, a config change rebuilds listeners and
// drops live connections.
type Notifier interface {
	UsersChanged()
	ConfigChanged(nodeID int64)
}

type nopNotifier struct{}

func (nopNotifier) UsersChanged()       {}
func (nopNotifier) ConfigChanged(int64) {}

type Service struct {
	st     *store.Store
	notify Notifier
}

func New(st *store.Store) *Service {
	return &Service{st: st, notify: nopNotifier{}}
}

// SetNotifier wires the hub in. Done after construction so the hub can hold a
// reference to the service without a circular dependency.
func (s *Service) SetNotifier(n Notifier) { s.notify = n }

func (s *Service) Store() *store.Store { return s.st }

var ErrInvalid = errors.New("invalid")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// A user name travels into sing-box's user list and comes back as a traffic
// counter key, so it has to survive JSON, a gRPC field and a log line without
// needing quoting. A tag is used the same way plus as a routing identifier.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)

func checkName(kind, name string) error {
	if !nameRE.MatchString(name) {
		return invalid("%s %q must be 1-32 chars of letters, digits, dot, dash or underscore, starting with a letter or digit", kind, name)
	}
	return nil
}

// ── users ───────────────────────────────────────────────────────────────────

// NewUser describes a user to create. Credentials are not in it: they are
// generated, never supplied, so there is no path by which a weak one gets in.
type NewUser struct {
	Name         string
	Note         string
	ExpiresAt    *time.Time
	TrafficLimit int64
}

func (s *Service) CreateUser(nu NewUser) (*store.User, error) {
	nu.Name = strings.TrimSpace(nu.Name)
	if err := checkName("user name", nu.Name); err != nil {
		return nil, err
	}
	if nu.TrafficLimit < 0 {
		return nil, invalid("traffic limit cannot be negative")
	}

	u := &store.User{
		Name:         nu.Name,
		VlessUUID:    NewUUID(),
		Password:     NewPassword(),
		SSPassword:   NewSSPassword(),
		SubToken:     NewSubToken(),
		Enabled:      true,
		ExpiresAt:    nu.ExpiresAt,
		TrafficLimit: nu.TrafficLimit,
		Note:         nu.Note,
	}
	if err := s.st.CreateUser(u); err != nil {
		return nil, err
	}
	s.notify.UsersChanged()
	return u, nil
}

func (s *Service) Users() ([]*store.User, error) { return s.st.Users() }

func (s *Service) User(id int64) (*store.User, error) { return s.st.User(id) }

// UpdateUser applies an edit. The caller passes the user it read and modified;
// traffic_used is not written, so a stale value in the form cannot reset
// someone's accumulated usage.
func (s *Service) UpdateUser(u *store.User) error {
	u.Name = strings.TrimSpace(u.Name)
	if err := checkName("user name", u.Name); err != nil {
		return err
	}
	if u.TrafficLimit < 0 {
		return invalid("traffic limit cannot be negative")
	}
	if err := s.st.UpdateUser(u); err != nil {
		return err
	}
	s.notify.UsersChanged()
	return nil
}

func (s *Service) DeleteUser(id int64) error {
	if err := s.st.DeleteUser(id); err != nil {
		return err
	}
	s.notify.UsersChanged()
	return nil
}

// UserInboundIDs is the set of inbounds this user may use. Empty means every
// inbound — the common case, and the reason the restriction is stored as rows
// that exist rather than as a flag.
func (s *Service) UserInboundIDs(id int64) ([]int64, error) {
	return s.st.UserInboundIDs(id)
}

// SetUserInbounds replaces that set. Passing none removes the restriction
// entirely rather than leaving the user with access to nothing, which is what
// clearing every checkbox in a UI means.
func (s *Service) SetUserInbounds(id int64, inboundIDs []int64) error {
	if _, err := s.st.User(id); err != nil {
		return err
	}
	if err := s.st.SetUserInbounds(id, inboundIDs); err != nil {
		return err
	}
	// The node's user lists are per inbound, so this changes what it must hold
	// even though no user record moved.
	s.notify.UsersChanged()
	return nil
}

func (s *Service) ResetUserTraffic(id int64) error {
	if err := s.st.ResetUserTraffic(id); err != nil {
		return err
	}
	// Resetting usage can bring a user back under their limit, which makes them
	// active again — the node has to hear about it.
	s.notify.UsersChanged()
	return nil
}

// ── nodes ───────────────────────────────────────────────────────────────────

// CreateNode returns the node and its join token. The token is shown once and
// only its hash is stored, so this return value is the only chance to record it.
func (s *Service) CreateNode(name, address, country string) (*store.Node, string, error) {
	name = strings.TrimSpace(name)
	address = strings.TrimSpace(address)
	if err := checkName("node name", name); err != nil {
		return nil, "", err
	}
	if address == "" {
		return nil, "", invalid("node address is required")
	}
	country = strings.ToUpper(strings.TrimSpace(country))
	if country == "" {
		country = "XX"
	}
	if len(country) != 2 {
		return nil, "", invalid("country must be a two-letter code")
	}

	token := NewNodeToken()
	hash, err := HashSecret(token)
	if err != nil {
		return nil, "", err
	}
	n := &store.Node{Name: name, TokenHash: hash, Address: address,
		Country: country, Enabled: true}
	if err := s.st.CreateNode(n); err != nil {
		return nil, "", err
	}
	return n, token, nil
}

func (s *Service) Nodes() ([]*store.Node, error) { return s.st.Nodes() }

func (s *Service) Node(id int64) (*store.Node, error) { return s.st.Node(id) }

func (s *Service) UpdateNode(n *store.Node) error {
	n.Name = strings.TrimSpace(n.Name)
	if err := checkName("node name", n.Name); err != nil {
		return err
	}
	if strings.TrimSpace(n.Address) == "" {
		return invalid("node address is required")
	}
	// Whether this edit turns the node off decides whether the node has to be
	// told. Renaming one must not: pushing a config rebuilds every listener and
	// drops every live connection, which is a strange price for fixing a typo.
	prev, err := s.st.Node(n.ID)
	if err != nil {
		return err
	}
	if err := s.st.UpdateNode(n); err != nil {
		return err
	}

	// Tags follow the node name, so "ss-tokyo" does not outlive a node called
	// tokyo. That rewrites the config the node runs, so it has to be pushed —
	// and the user lists keyed by those tags go with it.
	renamed := 0
	if prev.Name != n.Name {
		renamed, err = s.retagNodeInbounds(n.ID, n.Name)
		if err != nil {
			return err
		}
	}
	if prev.Enabled != n.Enabled || renamed > 0 {
		s.notify.ConfigChanged(n.ID)
	}
	// The address is what subscriptions point at, so an edit changes generated
	// configs; the node's own running config is otherwise unaffected.
	return nil
}

func (s *Service) RotateNodeToken(id int64) (string, error) {
	token := NewNodeToken()
	hash, err := HashSecret(token)
	if err != nil {
		return "", err
	}
	if err := s.st.RotateNodeToken(id, hash); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Service) DeleteNode(id int64) error { return s.st.DeleteNode(id) }

// ── inbounds ────────────────────────────────────────────────────────────────

func (s *Service) CreateInbound(nodeID int64, spec InboundSpec) (*store.Inbound, error) {
	node, err := s.st.Node(nodeID)
	if err != nil {
		return nil, err
	}

	// Tags are globally unique because they address an inbound in the config
	// pushed to a node, so leaving them to be typed by hand is a collision
	// waiting to happen. The panel already knows the node and the protocol,
	// which is exactly what makes a tag unique, so derive it by default and
	// keep the field as an override rather than a requirement.
	spec.Tag = strings.TrimSpace(spec.Tag)
	if spec.Tag == "" {
		spec.Tag = s.deriveInboundTag(spec.Protocol, node.Name)
	}
	if err := checkName("inbound tag", spec.Tag); err != nil {
		return nil, err
	}

	in, err := BuildInbound(spec)
	if err != nil {
		return nil, err
	}
	in.NodeID = nodeID
	// Not part of BuildInbound: this changes nothing the node is sent, only
	// what subscriptions point at.
	if err := CheckRelayAddress(spec.Address); err != nil {
		return nil, err
	}
	in.Address = strings.TrimSpace(spec.Address)
	if err := s.st.CreateInbound(in); err != nil {
		return nil, err
	}
	s.notify.ConfigChanged(nodeID)
	return in, nil
}

// deriveInboundTag builds "<protocol>-<node>", falling back to a numeric suffix
// when a node already has an inbound of that protocol. Sanitising the node name
// matters because it is free-form: "Tokyo #2" has to become something a tag
// accepts, and two nodes that sanitise to the same string still get distinct
// tags from the suffix loop.
func (s *Service) deriveInboundTag(protocol, nodeName string) string {
	base := protoSlug(protocol) + "-" + nodeSlug(nodeName)
	candidate := base
	for i := 2; ; i++ {
		if _, err := s.st.InboundByTag(candidate); errors.Is(err, store.ErrNotFound) {
			return candidate
		} else if err != nil {
			// A lookup failure is not worth failing creation over: hand back
			// the candidate and let the unique index be the real arbiter.
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", base, i)
	}
}

func (s *Service) Inbounds() ([]*store.Inbound, error) { return s.st.Inbounds() }

func (s *Service) NodeInbounds(nodeID int64) ([]*store.Inbound, error) {
	return s.st.NodeInbounds(nodeID)
}

func (s *Service) SetInboundEnabled(id int64, enabled bool) error {
	in, err := s.st.Inbound(id)
	if err != nil {
		return err
	}
	in.Enabled = enabled
	if err := s.st.UpdateInbound(in); err != nil {
		return err
	}
	s.notify.ConfigChanged(in.NodeID)
	return nil
}

func (s *Service) DeleteInbound(id int64) error {
	in, err := s.st.Inbound(id)
	if err != nil {
		return err
	}
	if err := s.st.DeleteInbound(id); err != nil {
		return err
	}
	s.notify.ConfigChanged(in.NodeID)
	return nil
}
