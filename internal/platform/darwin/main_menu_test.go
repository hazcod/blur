//go:build darwin

package darwin

import (
	"math"
	"testing"
	"time"
)

func TestApplicationMenuHasAboutAndExplicitQuitActions(t *testing.T) {
	items := applicationMenuItems("Blur")
	if len(items) != 3 {
		t.Fatalf("application menu has %d items, want About, Close Window and explicit Quit", len(items))
	}
	if items[0].title != "About Blur" || items[0].selector != "orderFrontStandardAboutPanel:" || items[0].key != "" {
		t.Fatalf("About menu action = %+v", items[0])
	}
	if items[1].title != "Close Window" || items[1].selector != "performClose:" || items[1].key != "q" || items[1].modifiers != menuCommandModifier || items[1].targetApplication {
		t.Fatalf("Close Window menu action = %+v", items[1])
	}
	if items[2].title != "Quit Blur" || items[2].selector != "terminate:" || items[2].key != "" || items[2].modifiers != 0 || !items[2].targetApplication {
		t.Fatalf("Quit menu action = %+v", items[2])
	}
}

func TestApplicationMenuRejectsMissingDisplayName(t *testing.T) {
	if items := applicationMenuItems("  "); len(items) != 0 {
		t.Fatalf("application menu for blank name = %+v, want no unsafe menu", items)
	}
}

func TestMenuBarMenuOffersInteractSettingsPauseAndQuit(t *testing.T) {
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	items := menuBarItems(true, time.Time{}, now, true)
	if len(items) != 9 {
		t.Fatalf("status menu has %d entries, want state, Interact, Settings, Pause for, Pause and Quit", len(items))
	}
	if items[0].title != "Capture is on" || items[0].enabled || items[0].selector != "" {
		t.Fatalf("capture state item = %+v", items[0])
	}
	if !items[1].separator {
		t.Fatalf("expected separator after capture status, got %+v", items[1])
	}
	if items[2].title != "Interact" || items[2].selector != "openInteract:" || items[2].target != statusTargetInteract {
		t.Fatalf("Interact action = %+v", items[2])
	}
	if items[3].title != "Settings…" || items[3].selector != "openSettings:" || items[3].target != statusTargetSettings {
		t.Fatalf("Settings action = %+v", items[3])
	}
	pause := items[5]
	if !items[4].separator || pause.title != "Pause for" || pause.hidden || len(pause.submenu) != 3 {
		t.Fatalf("Pause for submenu = %+v", pause)
	}
	for i, want := range []string{"15 minutes", "1 hour", "Until tomorrow"} {
		sub := pause.submenu[i]
		if sub.title != want || sub.selector != "pauseFor:" || sub.target != statusTargetPause || sub.tag != i || !sub.enabled {
			t.Fatalf("submenu %d = %+v", i, sub)
		}
	}
	if items[6].title != "Pause capture" || items[6].selector != "toggleCapture:" || items[6].target != statusTargetCapture {
		t.Fatalf("Pause action = %+v", items[6])
	}
	if !items[7].separator || items[8].title != "Quit Blur" || items[8].selector != "quitBlur:" || items[8].target != statusTargetQuit {
		t.Fatalf("Quit action = %+v", items[8])
	}
	paused := menuBarItems(false, time.Time{}, now, true)
	if paused[0].title != "Capture is paused" || paused[6].title != "Resume capture" || !paused[5].hidden {
		t.Fatalf("paused menu = %+v / %+v / %+v", paused[0], paused[5], paused[6])
	}
}

func TestPauseSubmenuTagsIndexTheDurationTable(t *testing.T) {
	var got []time.Duration
	statusPauseFor = func(d time.Duration) { got = append(got, d) }
	defer func() { statusPauseFor = nil }()
	for tag := -1; tag <= len(statusPauseOptions); tag++ {
		dispatchStatusPause(tag)
	}
	want := []time.Duration{15 * time.Minute, time.Hour, PauseUntilTomorrow}
	if len(got) != len(want) {
		t.Fatalf("dispatched %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dispatched %v, want %v", got, want)
		}
	}
}

func TestCaptureStateTitleShowsTimedPauseEnd(t *testing.T) {
	now := time.Date(2026, 11, 11, 13, 0, 0, 0, time.UTC)
	cases := []struct {
		until time.Time
		want  string
	}{
		{time.Time{}, "Capture is paused"},
		{time.Date(2026, 11, 11, 14, 30, 0, 0, time.UTC), "Capture paused until 14:30"},
		{time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC), "Capture paused until tomorrow 09:00"},
		{time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC), "Capture paused until Sat 09:00"},
	}
	for _, c := range cases {
		if got := captureStateTitle(false, c.until, now); got != c.want {
			t.Errorf("captureStateTitle(until=%v) = %q, want %q", c.until, got, c.want)
		}
	}
	if got := captureStateTitle(true, now.Add(time.Hour), now); got != "Capture is on" {
		t.Errorf("running title = %q", got)
	}
}

func TestMenuBarIconReflectsCaptureState(t *testing.T) {
	if got := menuBarIconAlpha(true); got != 1 {
		t.Fatalf("active capture icon alpha = %v, want 1", got)
	}
	if got := menuBarIconAlpha(false); got >= 1 || got <= 0 {
		t.Fatalf("paused capture icon alpha = %v, want a visible faded value", got)
	}
}

func TestMenuBarUsesThePackagedApplicationIcon(t *testing.T) {
	if statusMenuIconSelector != "imageNamed:" || statusMenuIconName != "AppIcon" {
		t.Fatalf("menu bar icon lookup = %q(%q), want the bundled AppIcon asset", statusMenuIconSelector, statusMenuIconName)
	}
	if statusMenuIconPointSize != 20 {
		t.Fatalf("menu bar icon point size = %d, want a clearly visible 20-point status-bar icon", statusMenuIconPointSize)
	}
}

func TestStatusQuickCaptureCallbackDispatchesToTheGoUIHandler(t *testing.T) {
	called := false
	if _, err := getStatusMenuTarget(func() { called = true }, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	dispatchStatusQuickCapture()
	if !called {
		t.Fatal("Quick Capture menu action did not dispatch to the GoUI handler")
	}
}

func TestWindowContentSizeRejectsInvalidDimensions(t *testing.T) {
	for _, dimensions := range [][2]float64{{0, 216}, {700, 0}, {700, 39}, {math.NaN(), 216}, {700, math.Inf(1)}, {2000, 216}} {
		if validWindowContentSize(dimensions[0], dimensions[1]) {
			t.Fatalf("accepted invalid native size %v", dimensions)
		}
	}
	if !validWindowContentSize(700, 480) {
		t.Fatal("rejected quick-panel answer size")
	}
	if !validWindowContentSize(560, 64) {
		t.Fatal("rejected compact Quick Capture size")
	}
}

func TestQuickPanelTopCenterAnchorsToNotchEdge(t *testing.T) {
	x, y := quickPanelTopLeft(100, 40, 1800, 1000, 640, 84, 0)
	if x != 680 || y != 1040 {
		t.Fatalf("quick panel top-left = (%.1f, %.1f), want (680, 1040)", x, y)
	}
}

func TestQuickPanelRoundsOnlyItsLowerEdge(t *testing.T) {
	if quickPanelMaskedCorners != 3 {
		t.Fatalf("Quick Panel masked corners = %d, want only lower corners", quickPanelMaskedCorners)
	}
}

func TestWindowContentSizeAcceptsQuickPanelSizes(t *testing.T) {
	for _, dimensions := range [][2]float64{{520, 80}, {520, 82}, {520, 136}, {820, 520}} {
		if !validWindowContentSize(dimensions[0], dimensions[1]) {
			t.Fatalf("rejected Quick Capture size %v", dimensions)
		}
	}
}
