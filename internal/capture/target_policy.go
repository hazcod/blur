package capture

import "github.com/hazcod/blur/internal/appidentity"

// TargetAllowed applies the same canonical app/domain deny and default-disabled
// allow rules as Pipeline.Capture to an already-verified source identity. It does
// not establish foreground, secure-field, private-window or user-presence state.
// Callers must verify those independently before handing content to storage.
func TargetAllowed(exclusions Exclusions, appID, domain string) bool {
	policy, ok := canonicalExclusions(exclusions)
	if !ok {
		return false
	}
	app, ok := normalizeAppID(appID)
	if !ok || app == "" {
		return false
	}
	host, ok := normalizeDomain(domain)
	if !ok {
		return false
	}
	if appidentity.IsBrowserApp(app) && host == "" && len(policy.Domains)+len(policy.IncludedDomains)+len(policy.RequiredDomains) > 0 {
		return false
	}
	return !excluded(app, host, policy) && included(app, host, policy)
}
