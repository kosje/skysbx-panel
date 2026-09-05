package sub

import (
	"net/http"
	"strings"
	"time"
)

// Format is the shape a subscription is rendered in.
type Format string

const (
	FormatHTML    Format = "html"    // the human-facing page
	FormatSingBox Format = "singbox" // sing-box JSON
	FormatClash   Format = "clash"   // mihomo YAML
	FormatBase64  Format = "base64"  // base64 of newline-joined share links
)

// nowFunc is swapped in tests.
var nowFunc = time.Now

// userAgentFormats maps a substring of the User-Agent to a format. Matched
// case-insensitively, longest first, so "sing-box" wins over a bare "box" and
// "clash-verge" is not mistaken for something else.
var userAgentFormats = []struct {
	needle string
	format Format
}{
	{"sing-box", FormatSingBox},
	{"singbox", FormatSingBox},
	{"sfa", FormatSingBox}, // sing-box for Android
	{"sfi", FormatSingBox}, // sing-box for iOS
	{"sfm", FormatSingBox}, // sing-box for macOS
	{"sft", FormatSingBox}, // sing-box for tvOS
	{"hiddify", FormatSingBox},
	{"karing", FormatSingBox},

	{"clash-verge", FormatClash},
	{"clashmeta", FormatClash},
	{"clash.meta", FormatClash},
	{"flclash", FormatClash},
	{"mihomo", FormatClash},
	{"clash", FormatClash},
	{"stash", FormatClash},
}

// Detect picks a format for a request.
//
// The browser check looks at Accept, not User-Agent. Every client sends a
// User-Agent, and plenty of them contain "Mozilla" for historical reasons, so
// deciding "is this a person looking at a page" from the User-Agent hands the
// HTML page to command-line clients. What actually distinguishes a browser is
// that it asks for text/html.
func Detect(r *http.Request) Format {
	if q := strings.ToLower(r.URL.Query().Get("format")); q != "" {
		switch q {
		case "singbox", "sing-box":
			return FormatSingBox
		case "clash", "mihomo":
			return FormatClash
		case "base64", "v2ray":
			return FormatBase64
		case "html", "page":
			return FormatHTML
		}
	}

	ua := strings.ToLower(r.Header.Get("User-Agent"))
	for _, m := range userAgentFormats {
		if strings.Contains(ua, m.needle) {
			return m.format
		}
	}

	if prefersHTML(r.Header.Get("Accept")) {
		return FormatHTML
	}

	// Base64 share links are the format with the widest client support, so an
	// unrecognised client gets something it can probably use rather than an
	// error.
	return FormatBase64
}

// prefersHTML reports whether the request came from a browser.
//
// The marker is application/xhtml+xml, not text/html. Clients that are not
// browsers routinely send a catch-all like "text/html,*/*", and even "is
// text/html first" cannot tell that apart from a real browser. Every browser
// sends application/xhtml+xml on a navigation request and nothing else does.
//
// Getting this wrong is asymmetric. A browser that somehow omits the marker
// sees base64 text and can add ?format=html; a client that is handed the web
// page cannot connect at all and has nothing useful to report. So the test is
// deliberately strict, and anything unrecognised gets a configuration.
func prefersHTML(accept string) bool {
	return strings.Contains(strings.ToLower(accept), "application/xhtml+xml")
}

// ContentType is the type to serve a format as.
func ContentType(f Format) string {
	switch f {
	case FormatHTML:
		return "text/html; charset=utf-8"
	case FormatSingBox:
		return "application/json; charset=utf-8"
	case FormatClash:
		return "text/yaml; charset=utf-8"
	default:
		// Deliberately text/plain: browsers that reach this by accident should
		// show it, not download it.
		return "text/plain; charset=utf-8"
	}
}
