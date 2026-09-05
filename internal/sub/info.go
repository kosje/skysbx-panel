package sub

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/kosje/skysbx-panel/internal/store"
)

// The Subscription-Userinfo header is the correct way to tell a client about
// usage and expiry, and the major clients read it. But each one shows it in a
// different corner and some show it nowhere until the profile is opened, so the
// numbers also go where every client puts them in front of the user: the name
// of each server in the list.
//
// An earlier attempt added them as separate unreachable entries, the way most
// panels do. They read as broken servers.

// label is what a client shows for one entry: which server it is, whose account
// it is, and what is left of that account.
//
// Only the link-list format uses it. sing-box and Clash name proxies with the
// stable tag instead: their names are also group members and rule targets, and
// a usage figure baked into one changes on every fetch — the client then sees a
// different set of servers each time and loses whatever the user had selected.
func label(node, tag string, u *store.User, now time.Time) string {
	// The tag is the server's identity everywhere else in this panel, and a
	// derived one already reads "ss-tokyo". A hand-typed tag like "01" does
	// not, and in a list spanning several nodes that is unanswerable — so the
	// node name goes in front of the ones that do not already carry it.
	name := tag
	if node != "" && !strings.Contains(tag, node) {
		name = node + " " + tag
	}
	parts := []string{name, u.Name}

	if u.TrafficLimit > 0 {
		parts = append(parts, fmt.Sprintf("%s/%s",
			humanBytes(u.TrafficUsed), humanBytes(u.TrafficLimit)))
	} else {
		parts = append(parts, fmt.Sprintf("%s/不限", humanBytes(u.TrafficUsed)))
	}

	if u.ExpiresAt != nil {
		// Local, like every date the panel shows. An expiry is stored as the
		// last second of a day in the operator's timezone, so formatting it in
		// UTC reads as the day after.
		day := u.ExpiresAt.Local().Format("2006-01-02")
		if days := int(u.ExpiresAt.Sub(now).Hours() / 24); days >= 0 {
			parts = append(parts, fmt.Sprintf("%s 到期", day))
		} else {
			parts = append(parts, fmt.Sprintf("%s 已过期", day))
		}
	} else {
		parts = append(parts, "长期")
	}

	return strings.Join(parts, " | ")
}

// humanBytes formats to two significant places, the way a client shows a quota.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}

// ProfileTitle is the Profile-Title header. Clients that honour it show this
// instead of the raw subscription URL, which is otherwise what the user sees in
// their list of profiles.
func ProfileTitle(name string) string {
	return "base64:" + base64.StdEncoding.EncodeToString([]byte(name))
}
