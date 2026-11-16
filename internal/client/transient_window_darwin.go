package client

import (
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/platform/darwin"
)

func prepareDesktopTransient(app ui.App, id string) error {
	if window := app.FindWindow(id); window != nil && window.PlatformWindow() != nil {
		return darwin.PrepareTransientWindow(window.PlatformWindow().NativeHandle())
	}
	return nil
}

func resizeMeetingOverlay(app ui.App, height float64) error {
	if window := app.FindWindow("meeting"); window != nil && window.PlatformWindow() != nil {
		return darwin.ResizeWindowContent(window.PlatformWindow().NativeHandle(), meetingOverlayWidth, height)
	}
	return nil
}
