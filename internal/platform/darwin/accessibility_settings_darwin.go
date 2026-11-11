package darwin

import (
	"errors"

	"github.com/golang-gui/goui/platform/darwin/frameworks/appkit"
	"github.com/golang-gui/goui/platform/darwin/frameworks/foundation"
)

var errAccessibilitySettingsUnavailable = errors.New("darwin: Accessibility settings unavailable")

func accessibilitySettingsURL() string {
	return "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility"
}

// OpenAccessibilitySettings opens macOS Privacy & Security → Accessibility.
// It never changes permissions; the user must enable Blur in System Settings.
func OpenAccessibilitySettings() error {
	workspace := appkit.NSWorkspaceClassId.SharedWorkspace()
	if workspace.ID == 0 {
		return errAccessibilitySettingsUnavailable
	}
	value := foundation.ToNSString(accessibilitySettingsURL())
	defer releaseNativeObject(value.ID)
	url := appkit.NSURLClassId.URLWithString(value)
	if url.ID == 0 || !workspace.OpenURL(url) {
		return errAccessibilitySettingsUnavailable
	}
	return nil
}
