package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/kosje/skysbx-panel/internal/store"
)

// renameNodeInbounds rewrites the tags of a node's inbounds after the node has
// been renamed, and reports how many it changed.
//
// Only tags that were derived from the old node name are touched — "ss-tokyo"
// becomes "ss-osaka", while a hand-typed "01" is left alone. A tag someone
// chose is not the panel's to rewrite, and guessing which of the two it is by
// pattern would get it wrong on exactly the names that matter.
//
// A rename is not free. The tag addresses the inbound in the configuration
// pushed to the node, so the node has to be told, which rebuilds its listeners
// and drops every live connection on it; and it addresses the inbound in the
// user lists keyed by it, so those go too. Callers do that.
func (s *Service) renameNodeInbounds(nodeID int64, oldName, newName string) (int, error) {
	oldSlug, newSlug := nodeSlug(oldName), nodeSlug(newName)
	if oldSlug == newSlug {
		return 0, nil
	}

	inbounds, err := s.st.NodeInbounds(nodeID)
	if err != nil {
		return 0, err
	}

	changed := 0
	for _, in := range inbounds {
		if !strings.Contains(in.Tag, oldSlug) {
			continue
		}
		tag := s.freeTag(strings.Replace(in.Tag, oldSlug, newSlug, 1))
		if tag == in.Tag {
			continue
		}

		// The tag lives in two places: the row the panel reads and the sing-box
		// object the node is sent. Leaving one behind means the node listens
		// under a tag no user list mentions, and every user on that inbound
		// stops authenticating.
		sb, err := ParseConfig(in)
		if err != nil {
			return changed, err
		}
		sb.Tag = tag
		cfg, err := json.Marshal(sb)
		if err != nil {
			return changed, err
		}
		in.Tag = tag
		in.Config = string(cfg)
		if err := s.st.UpdateInbound(in); err != nil {
			return changed, fmt.Errorf("rename inbound to %s: %w", tag, err)
		}
		changed++
	}
	return changed, nil
}

// freeTag returns want, or want with a numeric suffix if that tag is taken.
// Tags are globally unique because they address an inbound in a pushed
// configuration, so a rename that collides has to give way rather than fail —
// refusing would leave the node renamed and its inbounds half-renamed.
func (s *Service) freeTag(want string) string {
	candidate := want
	for i := 2; i < 100; i++ {
		_, err := s.st.InboundByTag(candidate)
		if errors.Is(err, store.ErrNotFound) {
			return candidate
		} else if err != nil {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", want, i)
	}
	return candidate
}

// nodeSlug is the form of a node name that appears in a derived tag.
func nodeSlug(name string) string {
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return -1
		}
	}, name)
	if slug == "" {
		return "node"
	}
	return slug
}
