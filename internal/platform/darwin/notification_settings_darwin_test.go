package darwin

import "testing"

func TestNotificationSettingsURLOpensNotificationsPane(t *testing.T) {
	if got := notificationSettingsURL(); got != "x-apple.systempreferences:com.apple.Notifications-Settings.extension" {
		t.Fatalf("notification settings URL = %q", got)
	}
}
