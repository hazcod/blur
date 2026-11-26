//go:build !darwin

package client

// dashboardOpenAtLaunch opens the Today window at launch. This platform has
// no status item, global menu, or Quick ask entry point; without the window
// the desktop runs invisibly with no way in.
func dashboardOpenAtLaunch() bool { return true }
