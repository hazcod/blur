package client

import (
	"github.com/hazcod/blur/internal/acceptance"
	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/preferences"
)

func captureExclusionsFromPreferences(config preferences.Config) capture.Exclusions {
	apps := append([]string(nil), config.ExcludedApps...)
	blurExcluded := false
	for _, app := range apps {
		if app == appidentity.BundleID {
			blurExcluded = true
			break
		}
	}
	if !blurExcluded {
		apps = append(apps, appidentity.BundleID)
	}
	result := capture.Exclusions{
		Apps:            apps,
		Domains:         append([]string(nil), config.ExcludedDomains...),
		IncludedApps:    append([]string(nil), config.IncludedApps...),
		IncludedDomains: append([]string(nil), config.IncludedDomains...),
		DefaultDisabled: config.CaptureDefaultDisabled,
	}
	if acceptance.Enabled {
		fixture, err := acceptance.Load()
		if err != nil {
			// A misconfigured fixture never falls back to the user's normal scope.
			result.DefaultDisabled = true
			result.IncludedApps = nil
			result.IncludedDomains = nil
		} else {
			result.RequiredApps = []string{fixture.AppID}
			result.RequiredDomains = []string{fixture.Domain}
		}
	}
	return result
}
