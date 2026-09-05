package web

import (
	"fmt"
	"html/template"
	"strconv"
	"strings"
	"time"
)

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"bytes": humanBytes,
		"date":  formatDate,
		"ago":   formatAgo,
		"pct":   percent,

		// Form values. These differ from their display counterparts by
		// rendering "unset" as an empty field rather than a dash or a zero —
		// putting "—" in a date input silently clears the date on save.
		"dateval": dateValue,
		"gibval":  gibValue,
	}
}

// dateValue fills a <input type=date>, whose only accepted format is this one.
func dateValue(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Local().Format("2006-01-02")
}

// gibValue fills the traffic-limit field. Trailing zeros are trimmed so a round
// 10 GiB limit reads "10" rather than "10.00" and survives a save unchanged.
func gibValue(n int64) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimRight(strings.TrimRight(
		strconv.FormatFloat(float64(n)/(1<<30), 'f', 2, 64), "0"), ".")
}

// humanBytes renders a byte count the way an operator reads it. Binary units,
// because that is what every other tool in this space reports.
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
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}

func formatDate(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return t.Local().Format("2006-01-02")
}

// formatAgo is for "last seen": an exact timestamp is less useful than knowing
// whether a node checked in a moment ago or last week.
func formatAgo(t *time.Time) string {
	if t == nil {
		return "never"
	}
	d := time.Since(*t)
	switch {
	case d < 90*time.Second:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// percent of a limit; a zero limit means unlimited, which has no percentage.
func percent(used, limit int64) string {
	if limit <= 0 {
		return ""
	}
	p := float64(used) / float64(limit) * 100
	if p > 100 {
		p = 100
	}
	return fmt.Sprintf("%.0f%%", p)
}
