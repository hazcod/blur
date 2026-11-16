package appidentity

import (
	"slices"
	"strings"
)

// MessagingApps lists, in sorted order, the lower-case bundle IDs and web
// domains of apps whose content is conversation: chat, direct messages and
// mail. Retrieval prefers them when a question asks to recap a conversation
// with a person. The list is policy for ranking only; it never grants capture.
var MessagingApps = []string{
	"com.apple.mail",
	"com.apple.mobilesms",
	"com.hnc.discord",
	"com.microsoft.outlook",
	"com.microsoft.teams",
	"com.microsoft.teams2",
	"com.tinyspeck.slackmacgap",
	"desktop.whatsapp.net",
	"discord.com",
	"mail.google.com",
	"net.whatsapp.whatsapp",
	"org.telegram.desktop",
	"org.whispersystems.signal-desktop",
	"outlook.live.com",
	"outlook.office.com",
	"outlook.office365.com",
	"ru.keepcoder.telegram",
	"slack.com",
	"teams.live.com",
	"teams.microsoft.com",
	"web.telegram.org",
	"web.whatsapp.com",
}

// IsMessagingApp reports whether an app bundle ID or web domain is a chat or
// mail app. Matching ignores case; a subdomain of a listed domain matches too
// (app.slack.com for slack.com).
func IsMessagingApp(appID string) bool {
	id := strings.ToLower(strings.TrimSpace(appID))
	for id != "" {
		if _, ok := slices.BinarySearch(MessagingApps, id); ok {
			return true
		}
		dot := strings.IndexByte(id, '.')
		if dot < 0 {
			return false
		}
		id = id[dot+1:]
		// A bare top-level label (for example "com") is never an app.
		if !strings.Contains(id, ".") {
			return false
		}
	}
	return false
}
