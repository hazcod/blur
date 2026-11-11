package client

import (
	"strings"
	"time"

	"github.com/golang-gui/goui/core/geometry"
	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/layout"
	"github.com/golang-gui/goui/platform/graphics"
	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/activity"
)

// Time page presets and per-app bars.

const (
	reportBarWidth    = 120
	reportBarHeight   = 4
	reportValueWidth  = 64
	reportCustomLabel = "Custom"
)

var reportPresetNames = []string{"Today", "Yesterday", "This week", "Last 7 days", "Last 30 days"}

// reportPresetRange returns the inclusive local dates for a preset. This week
// starts on Monday. ok is false for unknown names.
func reportPresetRange(name string, now time.Time) (start, end string, ok bool) {
	const layoutDate = "2006-01-02"
	day := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	switch name {
	case "Today":
		return day.Format(layoutDate), day.Format(layoutDate), true
	case "Yesterday":
		y := day.AddDate(0, 0, -1).Format(layoutDate)
		return y, y, true
	case "This week":
		back := (int(day.Weekday()) + 6) % 7
		return day.AddDate(0, 0, -back).Format(layoutDate), day.Format(layoutDate), true
	case "Last 7 days":
		return day.AddDate(0, 0, -6).Format(layoutDate), day.Format(layoutDate), true
	case "Last 30 days":
		return day.AddDate(0, 0, -29).Format(layoutDate), day.Format(layoutDate), true
	}
	return "", "", false
}

// reportSelectedPreset names the preset matching the dates, or "".
func reportSelectedPreset(start, end string, now time.Time) string {
	for _, name := range reportPresetNames {
		if s, e, _ := reportPresetRange(name, now); s == start && e == end {
			return name
		}
	}
	return ""
}

// reportZoneLine is the meta line under the Time title.
func reportZoneLine(zone string) string {
	if zone == "" || zone == "Local" {
		zone = localZoneName()
	}
	return "Times in " + zone
}

// reportBarFill is the filled width of a per-app bar. Any non-zero duration
// shows at least 2 px so it stays visible.
func reportBarFill(value, largest int64, width float32) float32 {
	if value <= 0 || largest <= 0 {
		return 0
	}
	if value > largest {
		value = largest
	}
	fill := width * float32(value) / float32(largest)
	return max(fill, 2)
}

func (d *desktop) reportBar(value, largest int64) ui.View {
	fill := reportBarFill(value, largest, reportBarWidth)
	p := d.tokens
	return ui.DrawingArea(func(painter gui.Painter, size geometry.Size) {
		painter.FillRect(geometry.Rect(0, 0, size.Width, size.Height), graphics.ColorOf(p.hairline))
		if fill > 0 {
			painter.FillRect(geometry.Rect(0, 0, min(fill, size.Width), size.Height), graphics.ColorOf(p.inkMuted))
		}
	}).MinSize(reportBarWidth, reportBarHeight).MaxSize(reportBarWidth, reportBarHeight)
}

func (d *desktop) reportAppRow(name string, value, largest int64) ui.View {
	return ui.HBox(
		wrapLabel(name).MainWeight(1),
		d.reportBar(value, largest),
		ui.HBox(wrapLabel("").MainWeight(1), wrapLabel(formatReportDuration(value)).Style(modern.MutedText)).MinWidth(reportValueWidth).MaxWidth(reportValueWidth),
	).Spacing(12).CrossAlign(layout.CrossCenter)
}

func (d *desktop) reportPresetRow(now time.Time) ui.View {
	selected := reportSelectedPreset(d.reportStart, d.reportEnd, now)
	buttons := make([]ui.View, 0, len(reportPresetNames)+1)
	for _, name := range reportPresetNames {
		preset := name
		buttons = append(buttons, choiceButton(preset, !d.reportCustom && selected == preset).Enabled(!d.loading).OnClick(func() {
			if start, end, ok := reportPresetRange(preset, time.Now()); ok {
				d.reportStart, d.reportEnd, d.reportCustom, d.status = start, end, false, ""
				d.reportReady, d.report = false, activity.ReportResult{}
				d.loadActivityReport()
			}
		}))
	}
	buttons = append(buttons, choiceButton(reportCustomLabel, d.reportCustom).Enabled(!d.loading).OnClick(func() {
		d.reportCustom = !d.reportCustom
		d.app.RequestUpdate()
	}))
	return ui.HBox(buttons...).Spacing(4).CrossAlign(layout.CrossCenter)
}

// Settings disclosure: each group shows its title and one sentence; the rest
// sits behind a flat Details toggle, collapsed by default.

// splitFirstSentence splits copy after its first ". " boundary.
func splitFirstSentence(text string) (first, rest string) {
	if i := strings.Index(text, ". "); i >= 0 {
		return text[:i+1], strings.TrimSpace(text[i+2:])
	}
	return text, ""
}

// flushButton is a flat button whose label aligns with the group text edge.
func flushButton(label string) *ui.ButtonView {
	return flatButton(label).Paddings(geometry.Insets{Left: 0, Right: 12, Top: 6, Bottom: 6})
}

// groupCopy returns the body label and, when there are details, the toggle and
// (if open) the detail labels.
func (d *desktop) groupCopy(group, body string, details ...string) []ui.View {
	rows := []ui.View{wrapLabel(body).Style(modern.MutedText)}
	var extra []string
	for _, text := range details {
		if text != "" {
			extra = append(extra, text)
		}
	}
	if len(extra) == 0 {
		return rows
	}
	open := d.privacyDetails[group]
	label := "Details"
	if open {
		label = "Hide details"
	}
	rows = append(rows, hug(flushButton(label).OnClick(func() {
		if d.privacyDetails == nil {
			d.privacyDetails = map[string]bool{}
		}
		d.privacyDetails[group] = !d.privacyDetails[group]
		d.app.RequestUpdate()
	})))
	if open {
		for _, text := range extra {
			rows = append(rows, wrapLabel(text).Style(modern.MutedText))
		}
	}
	return rows
}
