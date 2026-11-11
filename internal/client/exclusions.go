package client

import (
	"strings"

	"github.com/hazcod/blur/internal/capture"
)

const maxCaptureExclusionEntries = 32

func parseCaptureExclusionDrafts(appsDraft, domainsDraft string) (capture.Exclusions, bool) {
	apps, appsOK := parseCaptureExclusionLines(appsDraft)
	domains, domainsOK := parseCaptureExclusionLines(domainsDraft)
	if !appsOK || !domainsOK {
		return capture.Exclusions{}, false
	}
	return capture.NormalizeExclusions(capture.Exclusions{Apps: apps, Domains: domains})
}

func parseCaptureExclusionLines(draft string) ([]string, bool) {
	lines := strings.Split(draft, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
		if len(out) > maxCaptureExclusionEntries {
			return nil, false
		}
	}
	return out, true
}
