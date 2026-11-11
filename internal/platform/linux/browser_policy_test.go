package linux

import (
	"errors"
	"testing"
)

func TestLinuxBrowserCaptureRequiresAReadSafeNonPrivateWindowTitle(t *testing.T) {
	tests := []struct {
		name     string
		appID    string
		title    string
		titleErr error
		private  bool
	}{
		{"regular Chrome page", "google-chrome", "Project notes - Google Chrome", nil, false},
		{"incognito Chrome page", "google-chrome", "Project notes - Google Chrome (Incognito)", nil, true},
		{"private Firefox page", "firefox", "Project notes — Private Browsing - Mozilla Firefox", nil, true},
		{"unavailable title", "chromium", "", errors.New("fixture"), true},
		{"empty title", "chromium", "", nil, true},
		{"ordinary app ignores title", "org.gnome.TextEditor", "Private Browsing notes", nil, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isPrivateLinuxBrowserWindow(test.appID, test.title, test.titleErr); got != test.private {
				t.Fatalf("isPrivateLinuxBrowserWindow() = %t, want %t", got, test.private)
			}
		})
	}
}
