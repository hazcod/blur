package client

import (
	"log/slog"
	"time"
)

// A declarative update does not raise an already-mounted window. Show invokes
// the platform's normal deminiaturize, application activation and key-window
// path, retaining the existing window and its editing state.
func (d *desktop) presentDashboard(attempt int) {
	if d == nil || d.app == nil || !d.dashboardOpen || d.quickActivationPreferred() {
		return
	}
	if window := d.app.FindWindow("main"); window != nil {
		if err := window.Show(); err != nil {
			slog.Warn("dashboard could not be presented", "result", "failed")
			d.status = "Settings could not be brought to the front. Try opening it again."
			d.app.RequestUpdate()
		}
		return
	}
	if attempt >= 6 {
		d.status = "Settings could not be opened. Try opening it again."
		d.app.RequestUpdate()
		return
	}
	d.app.TimeoutFunc(25*time.Millisecond, func() { d.presentDashboard(attempt + 1) })
}
