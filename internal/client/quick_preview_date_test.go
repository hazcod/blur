package client

import (
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/today"
)

func TestQuickPreviewNeverPresentsHistoricalDayAsCurrentActivity(t *testing.T) {
	now := time.Now().In(time.Local)
	old := now.AddDate(0, 0, -1)
	d := &desktop{today: TodayView{Date: old.Format("2006-01-02"), Snapshot: today.Snapshot{Date: old.Format("2006-01-02"), TodoCandidates: []today.Todo{{Text: "Old historical task"}}}}, hourlySummaries: []chronicle.Summary{{Start: old, Text: "Old historical recap."}, {Start: now, Text: "Current saved recap."}}}
	var text string
	for _, group := range d.currentQuickPreviewGroups() {
		text += strings.Join(group.items, "\n")
	}
	if strings.Contains(text, "Old historical") || !strings.Contains(text, "Current saved recap.") {
		t.Fatal("historical dashboard selection leaked into current preview", text)
	}
}
