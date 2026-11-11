package darwin

import "testing"

func TestAccessibilitySettingsURLOpensPrivacyAccessibilityPane(t *testing.T) {
	const want = "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility"
	if got := accessibilitySettingsURL(); got != want {
		t.Fatalf("accessibilitySettingsURL() = %q, want %q", got, want)
	}
}
