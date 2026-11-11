//go:build windows

package capture

import (
	"context"
	"strings"

	"github.com/hazcod/blur/internal/platform"
	platformwindows "github.com/hazcod/blur/internal/platform/windows"
)

// NewWindowsPipeline installs the Windows UI Automation reader behind the
// same probe, exclusions, profile filtering, bounded traversal, and redaction
// rules used by the other platform pipelines.
func NewWindowsPipeline(source *platformwindows.Adapter, limits Limits, exclusions Exclusions, profiles []AppProfile) Pipeline {
	profileCopy := make([]AppProfile, len(profiles))
	for i, profile := range profiles {
		profileCopy[i] = AppProfile{AppID: strings.Clone(profile.AppID), IgnoreRoles: append([]string(nil), profile.IgnoreRoles...)}
	}
	exclusions.Apps = append([]string(nil), exclusions.Apps...)
	exclusions.Domains = append([]string(nil), exclusions.Domains...)
	return Pipeline{
		Source: source, Limits: limits, Exclusions: exclusions, Profiles: profileCopy,
		Read: func(ctx context.Context, probe platform.CaptureProbe, limits Limits) (rawFrame, error) {
			return readWindowsAX(ctx, source, probe, limits, newWindowsUIARoot)
		},
	}
}
