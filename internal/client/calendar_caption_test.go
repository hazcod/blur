package client

import (
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
)

func TestCalendarSummaryHeadingsAndAskCaptionsKeepTheirPeriodAndProvenance(t *testing.T) {
	start := time.Date(2023, 12, 31, 23, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		level, period, want string
		layer               assistant.Layer
	}{
		{"month", "2024-02", "Month summary · Feb 2024", assistant.LayerMonth},
		{"year", "2024", "Year summary · 2024", assistant.LayerYear},
	} {
		if got := summaryHeading(MemorySummaryHit{Level: tc.level, Period: tc.period, Start: start}); got != tc.want {
			t.Errorf("calendar summary mislabeled as another period: %q want%q", got, tc.want)
		}
		caption := askEvidenceCaption(assistant.Evidence{Layer: tc.layer, SourceIDs: []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}, CoverageLimited: true})
		if !strings.Contains(strings.ToLower(caption), tc.level+" recap") || !strings.Contains(caption, "2 saved sources") || !strings.Contains(caption, "limited") {
			t.Errorf("calendar evidence lost its derived-source and coverage labels: %q", caption)
		}
	}
}
