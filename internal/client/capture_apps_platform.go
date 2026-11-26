//go:build !linux

package client

import "github.com/hazcod/blur/internal/platform/darwin"

func (d *desktop) runningCaptureApps() []darwin.Application { return darwin.RunningApplications() }

func supportsApplicationChooser() bool { return true }
