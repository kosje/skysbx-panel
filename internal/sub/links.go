package sub

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/kosje/skysbx-panel/internal/store"
)

// ShareLinks renders one link per entry, in the ad-hoc URI formats the client
// ecosystem settled on. There is no specification for most of them; these are
// the forms clients actually parse.
func ShareLinks(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if link := shareLink(e); link != "" {
			out = append(out, link)
		}
	}
	return out
}

// Base64 renders the links as the single base64 blob most clients expect from a
// subscription URL, with any notice entries after the real servers.
//
// After, not before: a client importing a fresh subscription may make the first
// entry the current one, and that should be somewhere that carries traffic. A
// list this short is read whole anyway.
func Base64(entries []Entry, notices []string) string {
	lines := append(ShareLinks(entries), notices...)
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(lines, "\n")))
}

func shareLink(e Entry) string {
	host := hostPort(e.Address, e.Port)

	switch e.Protocol {
	case store.ProtoVLESS:
		q := url.Values{}
		q.Set("encryption", "none")
		q.Set("type", "tcp")
		q.Set("security", "reality")
		q.Set("sni", e.SNI)
		q.Set("pbk", e.PBK)
		if e.SID != "" {
			q.Set("sid", e.SID)
		}
		if e.FP != "" {
			q.Set("fp", e.FP)
		}
		if e.Flow != "" {
			q.Set("flow", e.Flow)
		}
		return "vless://" + e.UUID + "@" + host + "?" + q.Encode() + "#" + frag(e.Name)

	case store.ProtoAnyTLS:
		q := url.Values{}
		q.Set("security", "tls")
		q.Set("sni", e.SNI)
		if e.FP != "" {
			q.Set("fp", e.FP)
		}
		// No multiplex or smux parameter: AnyTLS multiplexes on its own, and
		// stacking another muxer on top breaks the connection.
		return "anytls://" + url.QueryEscape(e.Password) + "@" + host +
			"?" + q.Encode() + "#" + frag(e.Name)

	case store.ProtoShadowsocks:
		// SIP002: the userinfo is websafe-base64 of "method:password". Padding
		// is omitted because it is not URL-safe — several clients pass the
		// userinfo through a URL parser before decoding it.
		userinfo := base64.RawURLEncoding.EncodeToString(
			[]byte(e.Method + ":" + e.Password))
		return "ss://" + userinfo + "@" + host + "#" + frag(e.Name)
	}
	return ""
}

// hostPort joins an address and port, bracketing a bare IPv6 literal.
func hostPort(addr string, port int) string {
	if ip := net.ParseIP(addr); ip != nil && ip.To4() == nil {
		return "[" + addr + "]:" + strconv.Itoa(port)
	}
	return addr + ":" + strconv.Itoa(port)
}

// frag escapes a remark for use as a URI fragment. url.QueryEscape would turn
// spaces into '+', which clients display literally.
func frag(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// UserInfoHeader is the Subscription-Userinfo header every major client reads
// to show usage and expiry without opening a page.
func UserInfoHeader(used, total int64, expiresUnix int64) string {
	// upload is reported as 0 and everything as download: the panel keeps one
	// total per user, and splitting it arbitrarily would be a lie in a field
	// clients render to the user.
	s := fmt.Sprintf("upload=0; download=%d; total=%d", used, total)
	if expiresUnix > 0 {
		s += fmt.Sprintf("; expire=%d", expiresUnix)
	}
	return s
}
