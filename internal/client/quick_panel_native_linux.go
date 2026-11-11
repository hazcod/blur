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
func copyToPasteboard(string) error               { return errors.New("pasteboard unavailable") }
func registerQuickAskHotkey(string, func()) error { return errors.New("global shortcut unavailable") }
func unregisterQuickAskHotkey()                   {}

const quickAskHotkeySupported = false

func activateDesktopApp() {}

func orderWindowFrontQuietly(uintptr) error { return errors.New("unavailable") }

func setDesktopQuickPanelAlpha(ui.App, float64) error { return nil }

func commandKeyHeld() bool { return false }
