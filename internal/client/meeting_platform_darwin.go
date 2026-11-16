package client

import (
	"github.com/hazcod/blur/internal/hotkey"
	"github.com/hazcod/blur/internal/platform/darwin"
)

func installMeetingShortcut(open func()) error {
	return darwin.RegisterMeetingHotkey(hotkey.MeetingDefault, open)
}
func uninstallMeetingShortcut() { darwin.UnregisterMeetingHotkey() }
func updateMeetingMenu(open, stop func(), recording, active, paused, busy bool) error {
	return darwin.ConfigureMeetingMenu(open, recording, darwin.MeetingMenuControls{Active: active, Paused: paused, Busy: busy, Stop: stop})
}
