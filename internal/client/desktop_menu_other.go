//go:build !darwin && !linux && !windows

package client

import "github.com/golang-gui/goui/ui"
import "errors"

func supportsAccessibilitySettingsShortcut() bool { return false }
func watchDesktopActivation() (<-chan struct{}, func(), error) {
	return nil, func() {}, errors.New("activation watch unavailable")
}
func keepDesktopBackgroundAlive() (func(), error) { return func() {}, nil }
func openDesktopAccessibilitySettings() error     { return nil }

func installDesktopMainMenu() error                                                              { return nil }
func addDesktopQuitShortcut(window ui.WindowView, _ ui.App) ui.WindowView                        { return window }
func installDesktopStatusItem(_ ui.App, _ *pauseScheduler, _ bool, _, _, _ func(), _ func(bool)) {}
func setDesktopMenuBarVisible(_ ui.App, _ *pauseScheduler, _ bool, _, _, _ func()) error         { return nil }
func setDesktopMenuBarCapture(_ *pauseScheduler, _, _ bool)                                      {}
func addDesktopBackgroundClose(window ui.WindowView, _ ui.App, _ string, _ ...func()) ui.WindowView {
	return window
}
func menuBarSettingsView(_ *desktop) ui.View            { return nil }
func resizeDesktopQuickPanel(_ ui.App, _ float64) error { return nil }
func quickPanelSafeTopInset() float32                   { return 0 }
func desktopQuickPanelPointerInside(ui.App) bool        { return false }
func addDesktopQuickShortcut(window ui.WindowView, _ ui.App, onEscape func(), extra ...*ui.ShortcutView) ui.WindowView {
	escape := ui.Shortcut(ui.KeyEscape).OnActivate(onEscape)
	return window.Shortcuts(append(extra, escape)...)
}
