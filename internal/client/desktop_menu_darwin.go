package client

import (
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/platform/darwin"
)

func installDesktopMainMenu() error { return darwin.InstallApplicationMenu("Blur") }

func installDesktopStatusItem(app ui.App, pause *pauseScheduler, enabled bool, openInteract, openSettings, toggleCapture func(), onHover func(bool)) {
	if app == nil {
		return
	}
	app.Post(func() {
		installDesktopStatusItemAfterWindow(pause, enabled, openInteract, openSettings, toggleCapture, onHover)
	})
}

func installDesktopStatusItemAfterWindow(pause *pauseScheduler, enabled bool, openInteract, openSettings, toggleCapture func(), onHover func(bool)) {
	if err := darwin.InstallNotchTrigger(onHover, openInteract); err != nil {
		slog.Warn("notch Interact trigger unavailable", "result", "menu item remains available")
	} else {
		slog.Info("notch Interact trigger installed", "mode", "event_driven")
	}
	if !enabled {
		_ = darwin.ConfigureMenuBarItem(false, nil, nil, nil, nil, nil)
		return
	}
	if err := darwin.ConfigureMenuBarItem(true, openInteract, openSettings, toggleCapture, menuPauseCallback(pause), menuQuitCallback(pause)); err != nil {
		slog.Error("native menu bar item unavailable", "result", "failed")
	}
}

func placeDesktopQuickPanel(app ui.App, height float64) error {
	if app == nil {
		return errors.New("macos client: application unavailable")
	}
	window := app.FindWindow("quick")
	if window == nil {
		return errors.New("macos client: quick window unavailable")
	}
	return darwin.ResizeWindowContent(window.PlatformWindow().NativeHandle(), float64(quickPanelWidthForContent(height >= float64(quickPanelAnswerHeight))), height)
}

func setDesktopMenuBarVisible(app ui.App, pause *pauseScheduler, enabled bool, openInteract, openSettings, toggleCapture func()) error {
	if app == nil {
		return errors.New("macos client: application unavailable")
	}
	if !enabled {
		openInteract, openSettings, toggleCapture = nil, nil, nil
	}
	return darwin.ConfigureMenuBarItem(enabled, openInteract, openSettings, toggleCapture, menuPauseCallback(pause), menuQuitCallback(pause))
}

func resizeDesktopQuickPanel(app ui.App, height float64) error {
	if app == nil {
		return errors.New("macos client: application unavailable")
	}
	return placeDesktopQuickPanel(app, height)
}

func quickPanelSafeTopInset() float32 { return float32(darwin.QuickPanelSafeTopInset()) }

func desktopQuickPanelPointerInside(app ui.App) bool {
	if app == nil {
		return false
	}
	window := app.FindWindow("quick")
	if window == nil {
		return false
	}
	return darwin.PointerInsideWindow(window.PlatformWindow().NativeHandle())
}

func setDesktopMenuBarCapture(scheduler *pauseScheduler, running, activeAppCapturing bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("native capture menu update panicked", "panic_type", fmt.Sprintf("%T", recovered), "stack", string(debug.Stack()))
		}
	}()
	scheduler.observeCapture(running)
	darwin.UpdateMenuBarCaptureState(running, activeAppCapturing, scheduler.pausedUntil())
}

// menuPauseCallback returns the timed-pause handler, or nil when no scheduler
// is installed (the "Pause for" submenu is then hidden).
func menuPauseCallback(scheduler *pauseScheduler) func(time.Duration) {
	if scheduler != nil {
		return scheduler.pauseFor
	}
	return nil
}

func menuQuitCallback(scheduler *pauseScheduler) func() { return scheduler.cancel }

func openDesktopAccessibilitySettings() error { return darwin.OpenAccessibilitySettings() }

func supportsAccessibilitySettingsShortcut() bool { return true }

func watchDesktopActivation() (<-chan struct{}, func(), error) {
	return darwin.SubscribeSelfActivation()
}

func keepDesktopBackgroundAlive() (func(), error) { return darwin.DisableAutomaticTermination() }

func addDesktopBackgroundClose(window ui.WindowView, app ui.App, windowID string, onClose ...func()) ui.WindowView {
	if app == nil {
		return window
	}
	return window.OnCloseRequest(func(allow *bool) {
		if len(onClose) > 0 && onClose[0] != nil {
			onClose[0]()
		}
		err := keepDesktopRunningOnWindowClose(allow, func() error {
			closing := app.FindWindow(windowID)
			if closing == nil {
				return nil
			}
			return closing.PlatformWindow().Hide()
		})
		if err != nil {
			slog.Error("application window could not be hidden", "result", "failed")
		}
	})
}

func keepDesktopRunningOnWindowClose(allow *bool, hide func() error) error {
	if allow != nil {
		*allow = false
	}
	if hide == nil {
		return errors.New("macos client: window hide unavailable")
	}
	return hide()
}

func addDesktopQuitShortcut(window ui.WindowView, app ui.App) ui.WindowView {
	if app == nil {
		return window
	}
	quit := ui.Shortcut(ui.KeyQ).Modifiers(ui.ModCommand).OnActivate(func() {
		if main := app.FindWindow("main"); main != nil {
			_ = main.PlatformWindow().RequestClose()
		}
	})
	close := ui.Shortcut(ui.KeyW).Modifiers(ui.ModCommand).OnActivate(func() {
		if main := app.FindWindow("main"); main != nil {
			_ = main.PlatformWindow().RequestClose()
		}
	})
	return window.Shortcuts(quit, close)
}

func addDesktopQuickShortcut(window ui.WindowView, app ui.App, onEscape func(), extra ...*ui.ShortcutView) ui.WindowView {
	if app == nil {
		return window.Shortcuts(append(extra, ui.Shortcut(ui.KeyEscape).OnActivate(onEscape))...)
	}
	closeQuickCapture := func() {
		if quick := app.FindWindow("quick"); quick != nil {
			_ = quick.RequestClose()
		}
	}
	quit := ui.Shortcut(ui.KeyQ).Modifiers(ui.ModCommand).OnActivate(closeQuickCapture)
	close := ui.Shortcut(ui.KeyW).Modifiers(ui.ModCommand).OnActivate(closeQuickCapture)
	escape := ui.Shortcut(ui.KeyEscape).OnActivate(func() {
		if onEscape != nil {
			onEscape()
			return
		}
		closeQuickCapture()
	})
	bindings := append([]*ui.ShortcutView(nil), extra...)
	bindings = append(bindings, quit, close, escape)
	return window.Shortcuts(bindings...)
}

func menuBarSettingsView(d *desktop) ui.View {
	if d == nil {
		return nil
	}
	label := "Show Blur in the menu bar"
	if d.preferences.MenuBarItemEnabled {
		label = "Hide Blur from the menu bar"
	}
	return ui.Button(label).Style(modern.Flat).Enabled(!d.loading).OnClick(d.toggleMenuBarItem)
}
