package client

import (
	"strings"

	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/ingest"
)

func captureMenuDetail(running, known bool, appID string, excluded, included []string, defaultDisabled bool, health ingest.Health) string {
	if !running {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(appID), appidentity.BundleID) {
		detail := "Blur’s own windows are excluded"
		if health.LastProblemAt.After(health.LastReadableAt) {
			if issue := nativeReadIssueCopy(health.ReadIssue); issue != "" {
				detail += "; last capture problem: " + issue
			}
		}
		return detail
	}
	if !known {
		return "waiting for an active app"
	}
	if !captureIndicatorPolicyActive(running, known, appID, excluded, included, defaultDisabled) {
		return "this app is excluded by your capture rules"
	}
	switch health.State {
	case ingest.HealthReading:
		return "reading the current document"
	case ingest.HealthSaved, ingest.HealthUnchanged:
		if health.LastReadablePartial {
			return "last readable content was only partially captured"
		}
		return ""
	case ingest.HealthIdle:
		return "resting while this Mac is idle"
	case ingest.HealthEmpty:
		return "last app exposed no readable text"
	case ingest.HealthBrowserUnavailable:
		return "waiting for readable browser text"
	case ingest.HealthDomainUnknown:
		return "the active website could not be verified"
	case ingest.HealthPermission:
		if detail := nativeReadIssueCopy(health.ReadIssue); detail != "" {
			return detail
		}
		return "Accessibility access is required"
	case ingest.HealthStorageError:
		return "last save failed; open Settings for details"
	case ingest.HealthTimedOut:
		return "last app did not respond; retrying"
	case ingest.HealthUnavailable:
		if detail := nativeReadIssueCopy(health.ReadIssue); detail != "" {
			return detail
		}
		return "last app could not be read; retrying"
	case ingest.HealthCancelled:
		return "last capture was interrupted; retrying"
	case ingest.HealthExcluded:
		if health.LastProblemAt.After(health.LastReadableAt) {
			if detail := nativeReadIssueCopy(health.ReadIssue); detail != "" {
				return "last context was skipped by privacy rules; last capture problem: " + detail
			}
		}
		return "last context was skipped by privacy rules"
	default:
		return "waiting for readable content"
	}
}
