package client

import (
	"errors"
	"testing"
	"time"

	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/ui"
)

type dashboardPresentWindow struct {
	gui.Window
	shows int
	err   error
}

func (w *dashboardPresentWindow) Show() error { w.shows++; return w.err }

type dashboardPresentApp struct {
	quickDashboardApp
	main gui.Window
	next func()
}

func (a *dashboardPresentApp) FindWindow(id string) gui.Window {
	if id == "main" {
		return a.main
	}
	return nil
}
func (a *dashboardPresentApp) TimeoutFunc(_ time.Duration, fn func()) *ui.Timer {
	a.next = fn
	return nil
}

func TestSettingsMenuRaisesExistingDashboardEveryTime(t *testing.T) {
	w := &dashboardPresentWindow{}
	a := &dashboardPresentApp{main: w}
	d := &desktop{app: a, dashboardOpen: true, powerStatus: "checked"}
	for i := 1; i <= 2; i++ {
		d.openSettingsFromMenu()
		if w.shows != i || d.page != "Settings" || !d.dashboardOpen {
			t.Fatalf("Settings selection %d did not raise its existing window: shows=%d page=%q", i, w.shows, d.page)
		}
	}
}

func TestSettingsMenuRaisesNewWindowAfterMountAndHonorsClose(t *testing.T) {
	for _, closeBeforeMount := range []bool{false, true} {
		a := &dashboardPresentApp{}
		d := &desktop{app: a, powerStatus: "checked"}
		d.openSettingsFromMenu()
		if a.next == nil {
			t.Fatal("Settings did not schedule presentation after mounting")
		}
		w := &dashboardPresentWindow{}
		a.main = w
		if closeBeforeMount {
			d.dashboardOpen = false
		}
		a.next()
		want := 1
		if closeBeforeMount {
			want = 0
		}
		if w.shows != want {
			t.Fatalf("closeBeforeMount=%t shows=%d, want %d", closeBeforeMount, w.shows, want)
		}
	}
}

func TestSettingsPresentationFailureIsVisible(t *testing.T) {
	a := &dashboardPresentApp{main: &dashboardPresentWindow{err: errors.New("private native details")}}
	d := &desktop{app: a, powerStatus: "checked"}
	d.openSettingsFromMenu()
	if d.status != "Settings could not be brought to the front. Try opening it again." {
		t.Fatalf("missing safe window error: %q", d.status)
	}
}
