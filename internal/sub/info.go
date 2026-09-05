package sub

import (
	"encoding/base64"
	"fmt"
	"time"
)

// The Subscription-Userinfo header is the correct way to tell a client about
// usage and expiry, and every client worth naming reads it. But it shows up in
// a different corner of each one — a progress bar in Clash Verge, a line under
// the subscription name in v2rayNG — and some show it nowhere until the profile
// is opened.
//
// So the numbers also go in the server list, as entries that carry the text in
// their name and point nowhere. This is the convention the whole ecosystem
// settled on, and it is the one place every client puts in front of the user.
//
// They are only added to the link-list format. Clash and sing-box configs put
// proxies into selector and urltest groups, where an unreachable entry would
// either be picked or would skew latency testing.

// infoAddress is a documentation-reserved address (RFC 5737, TEST-NET-1) with a
// port nothing listens on: an info entry that is somehow selected fails to
// connect immediately, rather than hanging or reaching something real.
const infoAddress = "192.0.2.1"
const infoPort = 1

// InfoLinks renders the notice entries for one user. Empty when the user has
// neither a limit nor an expiry, since two lines reading "unlimited" are noise.
func InfoLinks(used, limit int64, expires *time.Time, now time.Time) []string {
	var out []string

	if limit > 0 {
		remaining := limit - used
		if remaining < 0 {
			remaining = 0
		}
		out = append(out, infoLink(fmt.Sprintf("已用 %s / 共 %s（剩 %s）",
			humanBytes(used), humanBytes(limit), humanBytes(remaining))))
	} else if used > 0 {
		out = append(out, infoLink(fmt.Sprintf("已用 %s（不限量）", humanBytes(used))))
	}

	if expires != nil {
		// Local, like every other date the panel shows. An expiry is stored as
		// the last second of a day in the operator's timezone, so formatting it
		// in UTC reads as the day after — the panel and the client would then
		// disagree by one day about when the account ends.
		day := expires.Local().Format("2006-01-02")
		days := int(expires.Sub(now).Hours() / 24)
		switch {
		case days < 0:
			out = append(out, infoLink(fmt.Sprintf("已于 %s 到期", day)))
		case days == 0:
			out = append(out, infoLink(fmt.Sprintf("%s 到期（今天）", day)))
		default:
			out = append(out, infoLink(fmt.Sprintf("%s 到期（剩 %d 天）", day, days)))
		}
	}
	return out
}

// infoLink wraps text as a Shadowsocks URI, which is the shortest form every
// client parses and the only one that needs no keys or fingerprints to look
// well-formed.
func infoLink(text string) string {
	cred := base64.RawURLEncoding.EncodeToString(
		[]byte("aes-128-gcm:skysbx-notice"))
	return fmt.Sprintf("ss://%s@%s:%d#%s", cred, infoAddress, infoPort, frag(text))
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
