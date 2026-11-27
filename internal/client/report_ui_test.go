package client

import (
	"fmt"
	"github.com/golang-gui/goui/gui"
	"github.com/hazcod/blur/internal/activity"
	"strings"
	"testing"
	"time"
)

func TestReportBarFill(t *testing.T) {
	cases := []struct {
		value, largest int64
		want           float32
	}{{0, 10, 0}, {10, 0, 0}, {10, 10, 120}, {5, 10, 60}, {1, 1000, 2}, {20, 10, 120}}
	for _, c := range cases {
		if got := reportBarFill(c.value, c.largest, 120); got != c.want {
			t.Errorf("fill(%d,%d)=%v want %v", c.value, c.largest, got, c.want)
		}
	}
}

func TestReportPresets(t *testing.T) {
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC) // Wednesday
	want := map[string][2]string{
		"Today": {"2026-11-11", "2026-11-11"}, "Yesterday": {"2026-11-10", "2026-11-10"},
		"This week": {"2026-11-09", "2026-11-11"}, "Last 7 days": {"2026-11-05", "2026-11-11"},
		"Last 30 days": {"2026-10-13", "2026-11-11"},
	}
	for name, w := range want {
		s, e, ok := reportPresetRange(name, now)
		if !ok || s != w[0] || e != w[1] {
			t.Errorf("%s = %s..%s", name, s, e)
		}
		if reportSelectedPreset(w[0], w[1], now) == "" {
			t.Errorf("%s not matched", name)
		}
	}
	if got := reportZoneLine("Europe/Brussels"); got != "Times in Europe/Brussels" {
		t.Error(got)
	}
	if first, rest := splitFirstSentence("A b. C d."); first != "A b." || rest != "C d." {
		t.Error(first, rest)
	}
}

func timeReportWidgetTree(t *testing.T, d *desktop) (map[string][]*gui.Button, string) {
	t.Helper()
	root := mountVoicePopupView(t, d.timeReportView())
	buttons := map[string][]*gui.Button{}
	var text strings.Builder
	var walk func(gui.Widget)
	walk = func(w gui.Widget) {
		if label, ok := w.(*gui.Label); ok {
			text.WriteString(label.Text())
			text.WriteByte('\n')
		}
		if button, ok := w.(*gui.Button); ok {
			if label, ok := button.Child().(*gui.Label); ok {
				buttons[label.Text()] = append(buttons[label.Text()], button)
			}
		}
		for _, child := range w.Children() {
			walk(child)
		}
	}
	walk(root)
	return buttons, text.String()
}
func TestLongTimeNativeAppRowsPageWithoutChangingTotal(t *testing.T) {
	app := &meetingOperationTestApp{posted: make(chan func(), 16)}
	d := &desktop{app: app, page: "Time", tokens: themePalette(false), reportReady: true, reportStart: "2024-01-01", reportEnd: "2024-12-31", reportZone: "UTC", report: activity.ReportResult{Eligible: int64(45 * time.Minute)}}
	for i := 0; i < 45; i++ {
		d.report.Apps = append(d.report.Apps, activity.Bucket{Key: fmt.Sprintf("reader-%02d", i), Duration: int64(time.Minute)})
	}
	buttons, text := timeReportWidgetTree(t, d)
	if strings.Count(text, "reader-") != 20 || !strings.Contains(text, "reader-00") || strings.Contains(text, "reader-20") || !strings.Contains(text, "45m") {
		t.Fatalf("unbounded first annual report page: %s", text)
	}
	clickMemoryButton(t, buttons, "Next apps")
	buttons, text = timeReportWidgetTree(t, d)
	if strings.Count(text, "reader-") != 20 || !strings.Contains(text, "reader-20") || strings.Contains(text, "reader-00") || !strings.Contains(text, "45m") {
		t.Fatalf("second annual report page: %s", text)
	}
	clickMemoryButton(t, buttons, "Next apps")
	buttons, text = timeReportWidgetTree(t, d)
	if strings.Count(text, "reader-") != 5 || !strings.Contains(text, "reader-44") || !strings.Contains(text, "45m") {
		t.Fatalf("last annual report page: %s", text)
	}
	clickMemoryButton(t, buttons, "Previous apps")
	_, text = timeReportWidgetTree(t, d)
	if !strings.Contains(text, "reader-20") || strings.Contains(text, "reader-44") {
		t.Fatalf("previous annual report page: %s", text)
	}
}
