package appidentity

import (
	"slices"
	"strings"
	"testing"
)

func TestMessagingAppsAreSortedLowerCaseAndUnique(t *testing.T) {
	if !slices.IsSorted(MessagingApps) {
		t.Fatalf("MessagingApps must stay sorted for binary search: %v", MessagingApps)
	}
	for i, id := range MessagingApps {
		if id != strings.ToLower(id) || i > 0 && MessagingApps[i-1] == id {
			t.Fatalf("MessagingApps entry %q is not lower-case and unique", id)
		}
	}
}

func TestIsMessagingApp(t *testing.T) {
	for _, id := range []string{
		"com.tinyspeck.slackmacgap", "com.microsoft.teams2", "com.apple.MobileSMS", "com.apple.mail",
		"com.microsoft.Outlook", "net.whatsapp.WhatsApp", "org.whispersystems.signal-desktop",
		"ru.keepcoder.Telegram", "com.hnc.Discord", "mail.google.com", "outlook.office.com", "app.slack.com", " COM.APPLE.MAIL ",
	} {
		if !IsMessagingApp(id) {
			t.Errorf("IsMessagingApp(%q) = false", id)
		}
	}
	for _, id := range []string{"", "com.apple.Terminal", "com.apple.finder", "notion.so", "com.kagi.kagimacOS", BundleID, "google.com", "com"} {
		if IsMessagingApp(id) {
			t.Errorf("IsMessagingApp(%q) = true", id)
		}
	}
}
