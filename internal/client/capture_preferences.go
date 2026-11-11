package client

import (
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/preferences"
)

func captureExclusionsFromPreferences(config preferences.Config) capture.Exclusions {
	return capture.Exclusions{
		Apps:            append([]string(nil), config.ExcludedApps...),
		Domains:         append([]string(nil), config.ExcludedDomains...),
		IncludedApps:    append([]string(nil), config.IncludedApps...),
		IncludedDomains: append([]string(nil), config.IncludedDomains...),
		DefaultDisabled: config.CaptureDefaultDisabled,
	}
}
