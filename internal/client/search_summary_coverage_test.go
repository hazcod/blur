package client

import (
	"context"
	"strings"
	"testing"

	"github.com/golang-gui/goui/gui"
)

func TestNativeSearchSummaryDisclosesLimitedCoverage(t *testing.T) {
	for _, limited := range []bool{false, true} {
		w := mountVoicePopupView(t, summaryRow(MemorySummaryHit{Level: "year", Period: "2024", Excerpt: "Calendar activity.", CoverageLimited: limited}, nil))
		var labels strings.Builder
		var walk func(gui.Widget)
		walk = func(w gui.Widget) {
			if label, ok := w.(*gui.Label); ok {
				labels.WriteString(label.Text())
				labels.WriteByte('\n')
			}
			for _, child := range w.Children() {
				walk(child)
			}
		}
		walk(w)
		if strings.Contains(labels.String(), "Limited coverage") != limited {
			t.Fatalf("native summary coverage disclosure=%v, want %v: %s", !limited, limited, labels.String())
		}
	}
}

func TestNativeSearchPageAggregatesSummaryCoverage(t *testing.T) {
	corpus, c, _ := recordingClient(t)
	before, err := c.SearchMemoryPageContext(context.Background(), "pentest")
	if err != nil || len(before.Hits) == 0 || before.CoverageLimited {
		t.Fatalf("complete exact-search control failed: %+v %v", before, err)
	}
	// This legacy recap has no persisted input coverage receipt.
	seedRollupFor(t, corpus, "day:2026-11-10", "- Reviewed pentest release evidence.", "slack-dima-1")
	after, err := c.SearchMemoryPageContext(context.Background(), "pentest")
	if err != nil || len(after.Summaries) == 0 || !after.CoverageLimited {
		t.Fatalf("native Search hid incomplete summary input coverage: %+v %v", after, err)
	}
}
