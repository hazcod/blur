//go:build !darwin

package client

import "testing"

// Without a status item, global menu, or Quick ask entry point, the Today
// window must be part of the initial view; otherwise the desktop runs
// invisibly with no way in.
func TestDashboardIsOpenAtLaunch(t *testing.T) {
	d := newDesktop(&quickDashboardApp{}, nil, nil)
	if !d.dashboardOpen {
		t.Fatal("newDesktop launched without the Today window")
	}
}
