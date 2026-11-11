package capture

import "testing"

func TestPrivateBrowserWindowTitlesAreRejected(t *testing.T) {
	tests := []string{
		"Project plan - Google Chrome (Incognito)",
		"Project plan — Safari, Private Browsing",
		"Internal wiki - Mozilla Firefox (Private Browsing)",
		"Project plan - Microsoft Edge (InPrivate)",
		"Recherche — navigation privée",
		"Projekt — privates Fenster",
		"Project — Privat nettlesing",
		"Project — Yksityinen selaus",
		"Project — Tryb prywatny",
		"Project — Nabigatze pribatua",
		"Project — Gizli Gezinti",
		"Project — Pelayaran Peribadi",
	}
	for _, title := range tests {
		t.Run(title, func(t *testing.T) {
			if !isPrivateBrowserWindowTitle(title) {
				t.Fatalf("private browser title was not rejected: %q", title)
			}
		})
	}
}

func TestRegularBrowserWindowTitlesRemainEligible(t *testing.T) {
	for _, title := range []string{"Project plan - Google Chrome", "Project plan — Safari", "Project plan - Mozilla Firefox", "Privacy policy - Google Chrome", "Project on the private sector - Safari"} {
		if isPrivateBrowserWindowTitle(title) {
			t.Fatalf("regular browser title was rejected: %q", title)
		}
	}
}

func TestBrowserProfilesExcludeAddressAndTabChrome(t *testing.T) {
	profiles, ok := profilesWithBuiltins(nil)
	if !ok {
		t.Fatal("built-in profiles are invalid")
	}
	validated, ok := validateProfiles(profiles)
	if !ok {
		t.Fatal("built-in profiles did not validate")
	}
	for _, app := range []string{
		"com.apple.safari", "com.google.chrome", "org.mozilla.firefox", "com.microsoft.edgemac",
		"google-chrome", "chromium", "firefox", "brave-browser", "microsoft-edge",
		"chrome.exe", "msedge.exe", "firefox.exe", "brave.exe", "opera.exe", "vivaldi.exe", "librewolf.exe",
	} {
		for _, role := range []string{"AXTextField", "AXComboBox", "AXTab", "AXTabGroup"} {
			if !ignoredAXRole(app, role, validated) {
				t.Errorf("%s role %s is not filtered from capture", app, role)
			}
		}
	}
}
