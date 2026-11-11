package appidentity

import "testing"

func TestIsBrowserAppUsesExactKnownBundleIDs(t *testing.T) {
	for _, app := range []string{"com.apple.safari", "com.google.chrome", "org.mozilla.firefox", "com.microsoft.edgemac", "com.brave.browser", "company.thebrowser.browser", "com.vivaldi.vivaldi", "google-chrome", "chromium", "firefox-esr", "brave-browser", "msedge", "chrome.exe", "msedge.exe", "firefox.exe", "brave.exe", "opera.exe", "vivaldi.exe", "librewolf.exe"} {
		if !IsBrowserApp(app) {
			t.Errorf("known browser %q was not recognized", app)
		}
	}
	for _, app := range []string{"", "com.apple.safari.helper", "org.example.editor"} {
		if IsBrowserApp(app) {
			t.Errorf("non-browser %q was recognized", app)
		}
	}
}
