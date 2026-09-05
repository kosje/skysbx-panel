package web

import (
	"fmt"
	"html/template"
	"strings"

	"github.com/kosje/skysb-panel/internal/service"
)

// trafficChart renders a bar chart as inline SVG.
//
// Server-rendered rather than drawn by a charting library: fourteen bars do not
// justify a JavaScript dependency, a build step, or the bundle that comes with
// them. The output is styled with currentColor so it follows the page's theme
// without knowing anything about it.
func trafficChart(days []service.DailyTotal) template.HTML {
	const (
		width   = 640
		height  = 90
		gap     = 3
		minBar  = 1 // so a day with a little traffic is still visible
		baselne = 14
	)
	if len(days) == 0 {
		return ""
	}

	var peak int64
	for _, d := range days {
		if d.Bytes > peak {
			peak = d.Bytes
		}
	}

	barW := (width - gap*(len(days)-1)) / len(days)
	plot := height - baselne

	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %d %d" width="100%%" height="%d" `+
		`role="img" aria-label="每日流量" preserveAspectRatio="none">`,
		width, height, height)

	for i, d := range days {
		x := i * (barW + gap)
		h := minBar
		if peak > 0 {
			// int64 throughout: a busy day can exceed what float32 represents
			// exactly, and a chart that rounds is worse than one that does not.
			h = int(int64(plot) * d.Bytes / peak)
			if h < minBar {
				h = minBar
			}
		}
		opacity := "0.85"
		if d.Bytes == 0 {
			opacity = "0.18"
		}
		fmt.Fprintf(&b,
			`<rect x="%d" y="%d" width="%d" height="%d" rx="1" `+
				`fill="currentColor" opacity="%s"><title>%s: %s</title></rect>`,
			x, plot-h, barW, h, opacity,
			d.Day.Format("01-02"), humanBytes(d.Bytes))
	}

	// Only the ends are labelled. Fourteen dates along an axis this small is
	// unreadable, and the per-bar tooltips carry the rest.
	fmt.Fprintf(&b,
		`<text x="0" y="%d" font-size="9" fill="currentColor" opacity="0.5">%s</text>`+
			`<text x="%d" y="%d" font-size="9" fill="currentColor" opacity="0.5" `+
			`text-anchor="end">%s</text>`,
		height-3, days[0].Day.Format("01-02"),
		width, height-3, days[len(days)-1].Day.Format("01-02"))

	b.WriteString(`</svg>`)

	// The only interpolated values are formatted numbers and dates produced
	// above, so this is safe to mark as HTML.
	return template.HTML(b.String())
}
