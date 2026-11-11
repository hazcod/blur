package client

import (
	"errors"

	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/platform/darwin"
)

func quickPanelHandle(app ui.App) (uintptr, error) {
	if app == nil {
		return 0, errors.New("macos client: application unavailable")
	}
	window := app.FindWindow("quick")
	if window == nil {
		return 0, errors.New("macos client: quick window unavailable")
	}
	return window.PlatformWindow().NativeHandle(), nil
}

// animateDesktopQuickPanel animates the panel to height (width is constant),
// falling back to an immediate resize if the animated path errors.
func animateDesktopQuickPanel(app ui.App, height float64) error {
	handle, err := quickPanelHandle(app)
	if err != nil {
		return err
	}
	if err := darwin.AnimateWindowContentResize(handle, quickPanelWidth, height); err != nil {
		return darwin.ResizeWindowContent(handle, quickPanelWidth, height)
	}
	return nil
}

func fadeInDesktopQuickPanel(app ui.App) error {
	handle, err := quickPanelHandle(app)
	if err != nil {
		return err
	}
	return darwin.FadeInWindow(handle)
}

func desktopQuickPanelVisible(app ui.App) bool {
	handle, err := quickPanelHandle(app)
	return err == nil && darwin.WindowVisible(handle)
}

func desktopReduceMotion() bool { return darwin.ReduceMotionEnabled() }

func copyToPasteboard(text string) error { return darwin.CopyToPasteboard(text) }

func registerQuickAskHotkey(spec string, onPress func()) error {
	return darwin.RegisterQuickAskHotkey(spec, onPress)
}

func unregisterQuickAskHotkey() { darwin.UnregisterQuickAskHotkey() }

const quickAskHotkeySupported = true

func activateDesktopApp() { darwin.ActivateApplication() }

// orderWindowFrontQuietly shows a window without activating the app or making
// it key.
func orderWindowFrontQuietly(handle uintptr) error { return darwin.OrderWindowFrontQuietly(handle) }

func setDesktopQuickPanelAlpha(app ui.App, alpha float64) error {
	handle, err := quickPanelHandle(app)
	if err != nil {
		return err
	}
	return darwin.SetWindowAlpha(handle, alpha)
}

func commandKeyHeld() bool { return darwin.CommandKeyHeld() }
