package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kosje/skysbx-panel/internal/store"
)

// retagNodeInbounds re-derives the tags of every inbound on a node after the
// node has been renamed, and reports how many changed.
//
// Every one, not only the ones that happened to carry the old name. A tag is
// how an inbound is identified in the node's logs, in the panel's tables and in
// the name a client shows, and one reading "01" on a node called us01 answers
// none of those. The cost is that a hand-typed tag does not survive a rename;
// that is the trade, and the rename form says so.
//
// A rename is not free. The tag addresses the inbound in the configuration
// pushed to the node, so the node has to be told, which rebuilds its listeners;
// and it keys the user lists, which follow the same push. Callers do that.
func (s *Service) retagNodeInbounds(nodeID int64, newName string) (int, error) {
	// By id, not by tag: deriving a numeric suffix has to be deterministic, and
	// ordering by the thing being rewritten is not.
	inbounds, err := s.st.NodeInboundsByID(nodeID)
	if err != nil {
		return 0, err
	}
	if len(inbounds) == 0 {
		return 0, nil
	}

	// Tags are globally unique, so the ones on other nodes are off limits. The
	// ones on this node are not: they are all about to be reassigned.
	all, err := s.st.Inbounds()
	if err != nil {
		return 0, err
	}
	taken := make(map[string]bool, len(all))
	for _, in := range all {
		if in.NodeID != nodeID {
			taken[in.Tag] = true
		}
	}

	slug := nodeSlug(newName)
	var updates []store.Retag

	for _, in := range inbounds {
		base := protoSlug(in.Protocol) + "-" + slug
		tag := base
		for i := 2; taken[tag]; i++ {
			tag = fmt.Sprintf("%s-%d", base, i)
		}
		taken[tag] = true
		if tag == in.Tag {
			continue
		}

		// The tag lives in two places: the row the panel reads and the sing-box
		// object the node is sent. Leaving one behind means the node listens
		// under a tag no user list mentions, and every user on that inbound
		// stops authenticating.
		sb, err := ParseConfig(in)
		if err != nil {
			return 0, err
		}
		sb.Tag = tag
		cfg, err := json.Marshal(sb)
		if err != nil {
			return 0, err
		}
		updates = append(updates, store.Retag{ID: in.ID, Tag: tag, Config: string(cfg)})
	}

	if err := s.st.RetagInbounds(updates); err != nil {
		return 0, err
	}
	return len(updates), nil
}

// protoSlug is the protocol's form in a tag.
func protoSlug(protocol string) string {
	if protocol == store.ProtoShadowsocks {
		// "shadowsocks-tokyo" is a mouthful in every log line.
		return "ss"
	}
	return protocol
}

// nodeSlug is the form of a node name that appears in a tag.
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
