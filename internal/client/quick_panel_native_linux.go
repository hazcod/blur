//go:build linux

package client

import (
	"errors"

	"github.com/golang-gui/goui/ui"
)

func animateDesktopQuickPanel(app ui.App, height float64) error {
	return resizeDesktopQuickPanel(app, height)
}
func fadeInDesktopQuickPanel(ui.App) error        { return nil }
func desktopQuickPanelVisible(ui.App) bool        { return false }
func desktopReduceMotion() bool                   { return false }
func registerQuickAskHotkey(string, func()) error { return errors.New("global shortcut unavailable") }
func unregisterQuickAskHotkey()                   {}

const quickAskHotkeySupported = false

func orderWindowFrontQuietly(uintptr) error { return errors.New("unavailable") }

func setDesktopQuickPanelAlpha(ui.App, float64) error { return nil }

func commandKeyHeld() bool { return false }

// copyToPasteboard places text on the X11 CLIPBOARD selection. Blur owns the
// selection while it runs, so copied text stays available until replaced.
func copyToPasteboard(text string) error {
	app, err := currentDesktopApp()
	if err != nil {
		return err
	}
	return app.Clipboard().SetText(text)
}
