//go:build darwin

package client

import "github.com/hazcod/blur/internal/acceptance"

// dashboardOpenAtLaunch keeps the macOS menu-bar-first launch: the Today
// window appears on demand from the status item or Quick ask.
func dashboardOpenAtLaunch() bool { return acceptance.Enabled }
