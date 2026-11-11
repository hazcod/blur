package appidentity

// IsBrowserApp recognizes only known browser bundle IDs and executable names.
// It is used to apply browser-specific passive accessibility capture policy;
// it never authorizes navigation or history access.
func IsBrowserApp(id string) bool {
	switch id {
	case "com.apple.safari", "com.google.chrome", "com.google.chrome.canary",
		"org.mozilla.firefox", "com.brave.browser", "com.microsoft.edgemac",
		"company.thebrowser.browser", "com.operasoftware.opera", "com.vivaldi.vivaldi",
		"google-chrome", "chrome", "chromium", "chromium-browser", "firefox", "firefox-esr",
		"brave", "brave-browser", "microsoft-edge", "msedge", "opera", "vivaldi", "librewolf":
		return true
	case "chrome.exe", "msedge.exe", "firefox.exe", "brave.exe", "opera.exe", "vivaldi.exe", "librewolf.exe":
		return true
	default:
		return false
	}
}
