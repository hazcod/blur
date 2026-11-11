package client

import (
	"log/slog"
	"strings"
	"time"

	"github.com/golang-gui/goui/core/geometry"
	"github.com/golang-gui/goui/layout"
)

// Quick ask geometry, in points. The window height always fits its content:
// safe-area inset + top padding + composer + bottom padding, plus the measured
// body when there is one.
const (
	quickPanelWidth           = 560
	quickPanelChatWidth       = quickPanelWidth // retained: the panel no longer changes width
	quickPanelPadX            = 20
	quickPanelPadTop          = 10
	quickPanelPadBottom       = 10
	quickPanelComposerHeight  = 36
	quickPanelBodyGap         = 6
	quickPanelProgressHeight  = 2
	quickPanelMaxHeight       = 340
	quickPanelAnswerHeight    = quickPanelMaxHeight
	quickPanelStatusLine      = 16
	quickPanelPreviewRows     = 6
	interactPaintCornerRadius = 0
	quickConversationLimit    = 4
	quickConversationBytes    = 6000
)

// quickPanelFitHeight is the window height for a panel whose body measures
// bodyHeight (0 when there is no body). The result never exceeds
// quickPanelMaxHeight; taller content scrolls inside the panel.
func quickPanelFitHeight(safeTopInset float32, progress, hairline bool, bodyHeight float32) int {
	height := ceilPoints(safeTopInset) + quickPanelPadTop + quickPanelComposerHeight + quickPanelPadBottom
	if progress {
		height += quickPanelBodyGap + quickPanelProgressHeight
	}
	if hairline {
		height += quickPanelBodyGap + 1
	}
	if bodyHeight > 0 {
		height += quickPanelBodyGap + ceilPoints(bodyHeight)
	}
	return min(height, quickPanelMaxHeight)
}

func ceilPoints(v float32) int {
	if v <= 0 {
		return 0
	}
	n := int(v)
	if float32(n) < v {
		n++
	}
	return n
}

func quickPanelWidthForContent(bool) int { return quickPanelWidth }

// quickPanelHeight is the compact, composer-only height.
func quickPanelHeight(bool) int {
	return quickPanelFitHeight(quickPanelSafeTopInset(), false, false, 0)
}

func quickPanelCreationHeight() int { return quickPanelHeight(false) }

func quickPanelInsets() geometry.Insets {
	return geometry.Insets{Top: quickPanelSafeTopInset() + quickPanelPadTop, Right: quickPanelPadX, Bottom: quickPanelPadBottom, Left: quickPanelPadX}
}

// quickPanelExpanded reports whether an answer is on screen. A pending ask is
// not expanded: it only adds a status line until the answer exists.
func (d *desktop) quickPanelExpanded() bool { return d != nil && len(d.quickConversation) > 0 }

// quickPanelBusy is true while an answer exists or is being produced, which
// replaces the day recap.
func (d *desktop) quickPanelBusy() bool { return d.quickPanelExpanded() || d.quickAskPending }

func (d *desktop) quickPanelProgressing() bool {
	return d != nil && d.quickAskPending && d.askLoading
}

// quickPanelHasContent reports typed text, an answer, a pending ask or a
// save confirmation: anything an accidental pointer exit must not discard.
func (d *desktop) quickPanelHasContent() bool {
	if d == nil {
		return false
	}
	return trimmed(d.quickInput) != "" || d.quickPanelBusy() || d.askLoading || d.quickExtras().savedFlash
}

// quickPanelExtras is panel state that lives beside the desktop struct.
type quickPanelExtras struct {
	fitEpoch    uint64
	revealEpoch uint64
	lastEscape  time.Time
	lastEnter   time.Time
	// saveNoteFn replaces the note save in tests.
	saveNoteFn      func()
	savedFlash      bool
	savedEpoch      uint64
	expandedTurns   map[int]bool
	copied          bool
	copyEpoch       uint64
	progressRunning bool
	progressPhase   float32
	progressDrawn   float32
	reduceMotion    bool
	hotkeyStatus    string
	// snapshotRun keeps the panel on screen during the UI snapshot run, where
	// the app is not the active one and focus loss would otherwise hide it.
	snapshotRun bool
}

func (d *desktop) quickExtras() *quickPanelExtras { return &d.quickUI }

// quickPanelDesiredHeight fits the window to the content: the measured body
// when the laid-out widget exists, otherwise a conservative estimate.
func (d *desktop) quickPanelDesiredHeight() int {
	if d == nil {
		return quickPanelHeight(false)
	}
	body := d.quickPanelBodyEstimate()
	if body > 0 {
		if measured, ok := d.measureQuickPanelBody(); ok {
			body = measured
		}
	}
	return quickPanelFitHeight(quickPanelSafeTopInset(), d.quickPanelProgressing(), d.quickPanelHairline(), body)
}

// measureQuickPanelBody asks the laid-out body column for its natural height
// at the panel's content width.
func (d *desktop) measureQuickPanelBody() (float32, bool) {
	if d.app == nil {
		return 0, false
	}
	widget := d.app.FindWidget("quick", "quick-body")
	if widget == nil {
		return 0, false
	}
	width := float32(quickPanelWidth - 2*quickPanelPadX)
	measured := widget.Measure(layout.Constraint{Max: geometry.Size{Width: width, Height: layout.Inf}})
	if measured.Height <= 0 || measured.Height >= layout.Inf {
		return 0, false
	}
	return measured.Height, true
}

// quickPanelBodyEstimate is the body height used before layout is available.
// Zero means the panel has no body.
func (d *desktop) quickPanelBodyEstimate() float32 {
	showPreview := d.quickPanelShowsPreview()
	switch {
	case showPreview:
		rows, titles := 0, 0
		for _, group := range quickPreviewGroups(d.today.Snapshot, d.hourlySummaries) {
			if len(group.items) > 0 && rows < quickPanelPreviewRows {
				titles++
				rows += min(len(group.items), quickPanelPreviewRows-rows)
			}
		}
		if d.todayLoading {
			return quickPanelStatusLine
		}
		return float32(rows*22 + titles*26)
	case len(d.quickConversation) > 0:
		height := float32(0)
		for _, turn := range d.quickConversation {
			lines := len(turn.answer)/62 + 1
			height += 15 + 8 + float32(lines)*22 + 10 + 14 + 12
		}
		return min(height+50, quickPanelMaxHeight)
	case d.quickAskPending || d.quickExtras().savedFlash || quickPanelShowsDraftStatus(d.quickInput, d.status):
		return quickPanelStatusLine
	}
	return 0
}

// quickPanelHairline reports whether a divider sits between the composer and a
// list-like body (answer or recap); status lines have none.
func (d *desktop) quickPanelHairline() bool {
	return d.quickPanelShowsPreview() && quickPreviewHasRows(d) || len(d.quickConversation) > 0
}

func quickPreviewHasRows(d *desktop) bool {
	if d.todayLoading {
		return false
	}
	for _, group := range quickPreviewGroups(d.today.Snapshot, d.hourlySummaries) {
		if len(group.items) > 0 {
			return true
		}
	}
	return false
}

// quickPanelShowsPreview reports whether the hover day recap is the body.
func (d *desktop) quickPanelShowsPreview() bool {
	return quickPanelShowsDayPreview(d.quickPreview, d.quickPanelPinned, d.quickPanelBusy(), quickPanelShowsDraftStatus(d.quickInput, d.status))
}

// resizeQuickPanel schedules a fit once the next update has laid the content
// out. It is only called on state changes (submit, answer, status, dismiss),
// never per keystroke.
func (d *desktop) resizeQuickPanel(_ bool) {
	if d == nil || d.app == nil {
		return
	}
	x := d.quickExtras()
	x.fitEpoch++
	epoch := x.fitEpoch
	d.app.Post(func() {
		d.app.RequestUpdate()
		for _, delay := range []time.Duration{40 * time.Millisecond, 160 * time.Millisecond} {
			d.app.TimeoutFunc(delay, func() {
				if epoch == x.fitEpoch {
					d.fitQuickPanel()
				}
			})
		}
	})
}

// fitQuickPanel animates the visible panel to its content height.
func (d *desktop) fitQuickPanel() {
	if d == nil || d.app == nil || !desktopQuickPanelVisible(d.app) {
		return
	}
	d.startQuickProgress()
	if err := animateDesktopQuickPanel(d.app, float64(d.quickPanelDesiredHeight())); err != nil {
		slog.Warn("quick ask placement unavailable", "result", "failed")
	}
}

// startQuickProgress drives the indeterminate bar at about 30 fps while an ask
// is pending. Under Reduce Motion it stays a static line.
func (d *desktop) startQuickProgress() {
	x := d.quickExtras()
	if !d.quickPanelProgressing() || x.progressRunning {
		return
	}
	x.progressRunning = true
	x.reduceMotion = desktopReduceMotion()
	var tick func()
	tick = func() {
		if !d.quickPanelProgressing() {
			x.progressRunning = false
			return
		}
		if !x.reduceMotion {
			x.progressPhase = float32(time.Now().UnixMilli()%1200) / 1200
		}
		if widget := d.app.FindWidget("quick", "quick-progress"); widget != nil {
			widget.RequestPaint()
		}
		d.app.TimeoutFunc(33*time.Millisecond, tick)
	}
	tick()
}

func trimmed(s string) string { return strings.TrimSpace(s) }
