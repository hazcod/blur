package capture

import "strings"

const (
	maxProfiles       = 128
	maxProfileRoles   = 32
	maxProfileRoleLen = 64
)

// AppProfile selects AX roles whose values are UI chrome rather than useful
// user context. Filtering is performed before value callbacks are invoked;
// descendants remain eligible because useful content can live under controls.
type AppProfile struct {
	AppID       string
	IgnoreRoles []string
}

var commonChromeRoles = map[string]struct{}{
	"AXButton": {}, "AXCheckBox": {}, "AXRadioButton": {}, "AXPopUpButton": {},
	"AXMenuBar": {}, "AXMenu": {}, "AXMenuItem": {}, "AXToolbar": {},
	"AXStatusBar": {}, "AXScrollBar": {}, "AXProgressIndicator": {}, "AXSecureTextField": {},
}

func validateProfiles(profiles []AppProfile) (map[string]map[string]struct{}, bool) {
	if len(profiles) > maxProfiles {
		return nil, false
	}
	result := make(map[string]map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		app, ok := normalizeAppID(profile.AppID)
		if !ok || app == "" || len(profile.IgnoreRoles) > maxProfileRoles {
			return nil, false
		}
		if _, exists := result[app]; exists {
			return nil, false
		}
		roles := make(map[string]struct{}, len(profile.IgnoreRoles))
		for _, raw := range profile.IgnoreRoles {
			role := strings.TrimSpace(raw)
			if !validAXRole(role) {
				return nil, false
			}
			roles[role] = struct{}{}
		}
		result[app] = roles
	}
	return result, true
}

// BuiltinProfiles returns conservative app-specific chrome filters. The
// returned slices are independent so callers may safely compose overrides.
func BuiltinProfiles() []AppProfile {
	roles := []string{"AXTabGroup", "AXTab", "AXImage", "AXSeparator"}
	return []AppProfile{
		{AppID: "com.tinyspeck.slackmacgap", IgnoreRoles: append([]string(nil), roles...)},
		{AppID: "com.slack", IgnoreRoles: append([]string(nil), roles...)},
		{AppID: "com.apple.safari", IgnoreRoles: browserChromeRoles()},
		{AppID: "com.google.chrome", IgnoreRoles: browserChromeRoles()},
		{AppID: "com.google.chrome.canary", IgnoreRoles: browserChromeRoles()},
		{AppID: "org.mozilla.firefox", IgnoreRoles: browserChromeRoles()},
		{AppID: "com.brave.browser", IgnoreRoles: browserChromeRoles()},
		{AppID: "com.microsoft.edgemac", IgnoreRoles: browserChromeRoles()},
		{AppID: "company.thebrowser.browser", IgnoreRoles: browserChromeRoles()},
		{AppID: "com.operasoftware.opera", IgnoreRoles: browserChromeRoles()},
		{AppID: "com.vivaldi.vivaldi", IgnoreRoles: browserChromeRoles()},
		{AppID: "google-chrome", IgnoreRoles: browserChromeRoles()},
		{AppID: "chrome", IgnoreRoles: browserChromeRoles()},
		{AppID: "chromium", IgnoreRoles: browserChromeRoles()},
		{AppID: "chromium-browser", IgnoreRoles: browserChromeRoles()},
		{AppID: "firefox", IgnoreRoles: browserChromeRoles()},
		{AppID: "firefox-esr", IgnoreRoles: browserChromeRoles()},
		{AppID: "brave", IgnoreRoles: browserChromeRoles()},
		{AppID: "brave-browser", IgnoreRoles: browserChromeRoles()},
		{AppID: "microsoft-edge", IgnoreRoles: browserChromeRoles()},
		{AppID: "msedge", IgnoreRoles: browserChromeRoles()},
		{AppID: "opera", IgnoreRoles: browserChromeRoles()},
		{AppID: "vivaldi", IgnoreRoles: browserChromeRoles()},
		{AppID: "librewolf", IgnoreRoles: browserChromeRoles()},
		{AppID: "chrome.exe", IgnoreRoles: browserChromeRoles()},
		{AppID: "msedge.exe", IgnoreRoles: browserChromeRoles()},
		{AppID: "firefox.exe", IgnoreRoles: browserChromeRoles()},
		{AppID: "brave.exe", IgnoreRoles: browserChromeRoles()},
		{AppID: "opera.exe", IgnoreRoles: browserChromeRoles()},
		{AppID: "vivaldi.exe", IgnoreRoles: browserChromeRoles()},
		{AppID: "librewolf.exe", IgnoreRoles: browserChromeRoles()},
	}
}

func browserChromeRoles() []string {
	return []string{"AXTabGroup", "AXTab", "AXImage", "AXSeparator", "AXTextField", "AXComboBox", "AXToolbar"}
}

func profilesWithBuiltins(custom []AppProfile) ([]AppProfile, bool) {
	if _, ok := validateProfiles(custom); !ok {
		return nil, false
	}
	profiles := BuiltinProfiles()
	indices := make(map[string]int, len(profiles)+len(custom))
	for i, profile := range profiles {
		indices[profile.AppID] = i
	}
	for _, profile := range custom {
		app, _ := normalizeAppID(profile.AppID)
		if index, exists := indices[app]; exists {
			roles := make(map[string]struct{}, len(profiles[index].IgnoreRoles))
			for _, role := range profiles[index].IgnoreRoles {
				roles[role] = struct{}{}
			}
			for _, role := range profile.IgnoreRoles {
				role = strings.TrimSpace(role)
				if _, duplicate := roles[role]; duplicate {
					continue
				}
				if len(profiles[index].IgnoreRoles) == maxProfileRoles {
					return nil, false
				}
				roles[role] = struct{}{}
				profiles[index].IgnoreRoles = append(profiles[index].IgnoreRoles, role)
			}
			continue
		}
		indices[app] = len(profiles)
		copy := AppProfile{AppID: app, IgnoreRoles: append([]string(nil), profile.IgnoreRoles...)}
		profiles = append(profiles, copy)
	}
	if _, ok := validateProfiles(profiles); !ok {
		return nil, false
	}
	return profiles, true
}

func validAXRole(role string) bool {
	if len(role) < 3 || len(role) > maxProfileRoleLen || !strings.HasPrefix(role, "AX") {
		return false
	}
	for _, r := range role[2:] {
		if !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// isEditableTextRole prevents capture of unsent drafts, search queries, and
// selected values. These nodes are treated as sensitive by the tree reader,
// which prunes the whole subtree before asking the native API for children or
// text; read-only message/history nodes retain their normal static-text roles.
func isEditableTextRole(role string) bool {
	switch role {
	case "AXSecureTextField", "AXPasswordField", "AXTextField", "AXTextArea", "AXComboBox", "AXSearchField":
		return true
	default:
		return false
	}
}

func ignoredAXRole(app, role string, profiles map[string]map[string]struct{}) bool {
	if _, ignored := commonChromeRoles[role]; ignored {
		return true
	}
	_, ignored := profiles[app][role]
	return ignored
}
