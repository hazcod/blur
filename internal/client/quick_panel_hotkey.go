package client

import (
	"context"
	"log/slog"

	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/hotkey"
)

const quickAskHotkeyTaken = "That shortcut is taken by another app."

// applyQuickAskHotkey registers the saved shortcut (or removes it when off).
// A failure is reported through the settings group and never blocks startup.
func (d *desktop) applyQuickAskHotkey() {
	if d == nil || d.app == nil || !quickAskHotkeySupported {
		return
	}
	d.app.Post(func() {
		if err := registerQuickAskHotkey(d.preferences.QuickAskHotkey, d.toggleQuickPanel); err != nil {
			d.quickUI.hotkeyStatus = quickAskHotkeyTaken
			d.app.RequestUpdate()
			return
		}
		d.quickUI.hotkeyStatus = ""
	})
}

// setQuickAskHotkey registers spec first and only then persists it, so a
// shortcut another app owns is never saved.
func (d *desktop) setQuickAskHotkey(spec string) {
	if d == nil || d.app == nil || d.loading || spec == d.preferences.QuickAskHotkey {
		return
	}
	previous := d.preferences
	next := previous
	next.QuickAskHotkey = spec
	if err := registerQuickAskHotkey(spec, d.toggleQuickPanel); err != nil {
		_ = registerQuickAskHotkey(previous.QuickAskHotkey, d.toggleQuickPanel)
		d.quickUI.hotkeyStatus = quickAskHotkeyTaken
		d.app.RequestUpdate()
		return
	}
	if d.client != nil {
		if err := d.client.ConfigureApplicationPreferences(context.Background(), next); err != nil {
			slog.Warn("quick ask shortcut could not be saved", "result", "failed")
			_ = registerQuickAskHotkey(previous.QuickAskHotkey, d.toggleQuickPanel)
			d.quickUI.hotkeyStatus = "The shortcut could not be saved. The previous one remains active."
			d.app.RequestUpdate()
			return
		}
	}
	d.preferences = next
	d.quickUI.hotkeyStatus = ""
	d.app.RequestUpdate()
}

// quickAskShortcutsPerRow is how many shortcut chips share a row.
const quickAskShortcutsPerRow = 5

// quickAskShortcutRows splits the offered shortcuts into rows of at most size.
func quickAskShortcutRows(options []hotkey.Option, size int) [][]hotkey.Option {
	if size < 1 {
		size = 1
	}
	var rows [][]hotkey.Option
	for len(options) > 0 {
		n := min(size, len(options))
		rows = append(rows, options[:n])
		options = options[n:]
	}
	return rows
}

// quickAskShortcutNote is the one line that says what the current choice does.
func quickAskShortcutNote(spec string) string {
	if spec == "" {
		return "No shortcut set. Quick ask still opens from the notch and the Quick ask link in the window."
	}
	return "Press " + hotkey.Label(spec) + " in any app to open Quick ask, and again to close it."
}

// quickAskSettingsView is the Settings page group for the global shortcut: all
// choices are visible at once, the active one is marked.
func (d *desktop) quickAskSettingsView() ui.View {
	if !quickAskHotkeySupported {
		return nil
	}
	rows := []ui.View{
		wrapLabel("Quick ask").Style(styleSectionTitle),
		wrapLabel("Choose a global shortcut for the Quick ask panel.").Style(modern.MutedText),
		wrapLabel("Keyboard shortcut"),
	}
	for _, row := range quickAskShortcutRows(hotkey.Options(), quickAskShortcutsPerRow) {
		chips := make([]ui.View, 0, len(row))
		for _, option := range row {
			chip := choiceButton(option.Label, option.Spec == d.preferences.QuickAskHotkey)
			chip.OnClick(func() { d.setQuickAskHotkey(option.Spec) })
			chips = append(chips, chip)
		}
		rows = append(rows, ui.HBox(chips...).Spacing(8))
	}
	rows = append(rows, wrapLabel(quickAskShortcutNote(d.preferences.QuickAskHotkey)).Style(modern.MutedText))
	if d.quickUI.hotkeyStatus != "" {
		rows = append(rows, wrapLabel(d.quickUI.hotkeyStatus).Style(modern.MutedText))
	}
	return surfaceBox(14, 9, rows...)
}
