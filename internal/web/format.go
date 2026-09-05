package web

import (
	"fmt"
	"html/template"
	"time"
)

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"bytes": humanBytes,
		"date":  formatDate,
		"ago":   formatAgo,
		"pct":   percent,
	}
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
