package service

import (
	"github.com/kosje/skysbx-panel/internal/store"
)

// Subscription is everything needed to render one user's subscription.
type Subscription struct {
	User     *store.User
	Nodes    []*store.Node
	Inbounds []*store.Inbound
	// Allowed is nil when the user may use every inbound, which is the common
	// case for a single-tenant panel.
	Allowed map[int64]bool
}

// Subscription looks a user up by their subscription token.
//
// It returns ErrNotFound for an unknown token and nothing else: whether a token
// belongs to a disabled, expired or over-limit user is not something an
// unauthenticated caller should be able to tell apart. An inactive user gets a
// valid but empty subscription instead.
func (s *Service) Subscription(token string) (*Subscription, error) {
	u, err := s.st.UserBySubToken(token)
	if err != nil {
		return nil, err
	}
	nodes, err := s.st.Nodes()
	if err != nil {
		return nil, err
	}
	inbounds, err := s.st.Inbounds()
	if err != nil {
		return nil, err
	}
	ids, err := s.st.UserInboundIDs(u.ID)
	if err != nil {
		return nil, err
	}

	var allowed map[int64]bool
	if len(ids) > 0 {
		allowed = make(map[int64]bool, len(ids))
		for _, id := range ids {
			allowed[id] = true
		}
	}
	return &Subscription{User: u, Nodes: nodes, Inbounds: inbounds, Allowed: allowed}, nil
}
