package darwin

import (
	"errors"

	"github.com/golang-gui/goui/platform/darwin/frameworks/appkit"
	"github.com/golang-gui/goui/platform/darwin/frameworks/foundation"
)

func notificationSettingsURL() string {
	return "x-apple.systempreferences:com.apple.Notifications-Settings.extension"
}

// OpenNotificationSettings opens the macOS notification pane. It never changes
// permission; the user selects Blur and changes its access in System Settings.
func OpenNotificationSettings() error {
	var err error
	withAutoreleasePool(func() {
		workspace := appkit.NSWorkspaceClassId.SharedWorkspace()
		if workspace.ID == 0 {
			err = errors.New("darwin: notification settings unavailable")
			return
		}
		value := foundation.ToNSString(notificationSettingsURL())
		defer releaseNativeObject(value.ID)
		url := appkit.NSURLClassId.URLWithString(value)
		if url.ID == 0 || !workspace.OpenURL(url) {
			err = errors.New("darwin: notification settings unavailable")
		}
	})
	return err
}
