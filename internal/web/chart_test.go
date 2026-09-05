package web

import (
	"strings"
	"testing"
	"time"

	"github.com/kosje/skysb-panel/internal/service"
)

func days(values ...int64) []service.DailyTotal {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	out := make([]service.DailyTotal, len(values))
	for i, v := range values {
		out[i] = service.DailyTotal{Day: base.AddDate(0, 0, i), Bytes: v}
	}
	return out
}

func TestTrafficChartIsWellFormed(t *testing.T) {
	svg := string(trafficChart(days(0, 100, 5000, 250, 0, 900, 1<<30)))

	if !strings.HasPrefix(svg, "<svg ") || !strings.HasSuffix(svg, "</svg>") {
		t.Fatalf("not a complete svg element: %.60s…", svg)
	}
	if got := strings.Count(svg, "<rect "); got != 7 {
		t.Errorf("drew %d bars, want 7", got)
	}
	if strings.Count(svg, "<rect ") != strings.Count(svg, "</rect>") {
		t.Error("unbalanced rect elements")
	}
	if !strings.Contains(svg, `role="img"`) {
		t.Error("no accessible role")
	}
}

func TestEmptyHistoryDrawsNothing(t *testing.T) {
	if got := trafficChart(nil); got != "" {
		t.Errorf("expected no chart for no data, got %q", got)
	}
}

// A day with no traffic must stay visible as a gap. Drawing it at full height,
// or omitting it, turns an outage into something that looks normal.
func TestZeroDaysAreDrawnFaint(t *testing.T) {
	svg := string(trafficChart(days(0, 1000)))
	if !strings.Contains(svg, `opacity="0.18"`) {
		t.Error("a zero day was not drawn faint")
	}
	if !strings.Contains(svg, `opacity="0.85"`) {
		t.Error("a non-zero day was not drawn solid")
	}
}

// Every bar must be at least a pixel tall. A day with real but small traffic
// that renders as nothing reads as an outage.
func TestSmallValuesStayVisible(t *testing.T) {
	svg := string(trafficChart(days(1, 1<<40)))
	if strings.Contains(svg, `height="0"`) {
		t.Error("a non-zero day was drawn with zero height")
	}
}

// Byte counts are int64 and a busy day can exceed float32's exact range;
// scaling has to stay in integers.
func TestLargeValuesDoNotOverflow(t *testing.T) {
	svg := string(trafficChart(days(1<<62, 1<<61)))
	if strings.Contains(svg, "height=\"-") || strings.Contains(svg, "NaN") {
		t.Errorf("scaling overflowed: %s", svg)
	}
}
