package client

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/ingest"
)

type captureHealthState struct {
	mu     sync.RWMutex
	sample ingest.Health
}

func (c *Client) setCaptureHealth(sample ingest.Health) {
	if c == nil || !sample.Valid() {
		return
	}
	c.captureHealth.mu.Lock()
	old := c.captureHealth.sample
	if sample.LastReadableAt.IsZero() {
		sample.LastReadableAt = old.LastReadableAt
		sample.LastReadablePartial = old.LastReadablePartial
	}
	if sample.LastSavedAt.IsZero() {
		sample.LastSavedAt = old.LastSavedAt
	}
	if sample.LastProblemAt.IsZero() {
		sample.LastProblem, sample.LastProblemAt = old.LastProblem, old.LastProblemAt
		if sample.ReadIssue == (capture.ReadIssue{}) && old.LastProblemAt.After(old.LastReadableAt) {
			sample.ReadIssue = old.ReadIssue
		}
	}
	if sample.State == ingest.HealthSaved || sample.State == ingest.HealthUnchanged {
		sample.ReadIssue = capture.ReadIssue{}
	}
	c.captureHealth.sample = sample
	c.captureHealth.mu.Unlock()
	c.NotifyCaptureStateChanged()
}

func (c *Client) CaptureHealth() ingest.Health {
	if c == nil {
		return ingest.Health{}
	}
	c.captureHealth.mu.RLock()
	defer c.captureHealth.mu.RUnlock()
	return c.captureHealth.sample
}

func captureIssueForHealth(health ingest.Health) string {
	if health.LastProblemAt.After(health.LastReadableAt) {
		switch health.LastProblem {
		case ingest.HealthPermission, ingest.HealthBrowserUnavailable, ingest.HealthDomainUnknown, ingest.HealthEmpty, ingest.HealthTimedOut, ingest.HealthStorageError, ingest.HealthUnavailable:
			return string(health.LastProblem)
		}
	}
	if health.LastReadablePartial {
		return "partial_content"
	}
	return ""
}

func captureHealthActive(health ingest.Health) bool {
	return health.State == ingest.HealthReading || health.State == ingest.HealthSaved || health.State == ingest.HealthUnchanged
}

// nativeReadIssueCopy renders only the shared stage/status whitelist. No
// backend error message or app-provided AX value is accepted by this surface.
func nativeReadIssueCopy(issue capture.ReadIssue) string {
	if !issue.Valid() {
		return ""
	}
	var stage string
	switch issue.Stage {
	case capture.ReadStageScreenPermission:
		return "Screen Recording permission is required to read visible text in this app. Review Blur in System Settings → Privacy & Security → Screen Recording. Accessibility capture remains available in other apps."
	case capture.ReadStageScreenText:
		return "The on-device visible-text reader is unavailable in this app. Blur will retry; readable Accessibility text remains available in other apps."
	case capture.ReadStageScreenVisibility:
		return "Visible-text capture needs the focused window to be fully visible, unobscured, and within the image size limit. Blur will retry automatically."

	case capture.ReadStageSetup:
		stage = "The native Accessibility reader could not be initialized"
	case capture.ReadStageApplication:
		stage = "The active app’s Accessibility connection could not be read"
	case capture.ReadStageWindow:
		stage = "The focused window could not be read"
	case capture.ReadStageRole:
		stage = "The focused element’s Accessibility role could not be read"
	case capture.ReadStageSensitivity:
		stage = "The focused element’s privacy checks could not be completed"
	case capture.ReadStageChildren:
		stage = "The focused window’s Accessibility elements could not be read"
	case capture.ReadStageText:
		stage = "The focused app’s text could not be read"
	case capture.ReadStageRange:
		stage = "The focused app’s text range could not be read"
	case capture.ReadStageCapability:
		stage = "The app’s capture capability check did not finish"
	case capture.ReadStagePresence:
		stage = "The active user session check did not finish"
	case capture.ReadStageProbe:
		stage = "The active app’s identity check did not finish"
	case capture.ReadStageBrowserPage:
		stage = "The active browser page could not be verified"
	case capture.ReadStageBrowserDomain:
		stage = "The active website’s domain could not be verified"
	case capture.ReadStageStructure:
		stage = "The app’s document structure could not be read in time"
	case capture.ReadStageValidation:
		stage = "The app’s final capture checks did not finish"
	default:
		return ""
	}
	switch issue.Status {
	case -25211:
		return stage + " (AX -25211: APIDisabled). Review Blur in System Settings → Privacy & Security → Accessibility."
	case -25204:
		return stage + " (AX -25204: CannotComplete). The app did not complete the Accessibility request; Blur will retry automatically."
	case 0:
		if issue.Stage == capture.ReadStageWindow {
			return stage + ". Focus a document window in the app; Blur will retry automatically."
		}
		return stage + ". Blur will retry automatically."
	default:
		return fmt.Sprintf("%s (AX %d). Blur will retry automatically.", stage, issue.Status)
	}
}

func captureHealthCopy(health ingest.Health, running bool) string {
	if !running {
		copy := "Capture is paused."
		if health.LastReadablePartial {
			copy += " The last readable content was only partially captured."
		}
		return copy
	}
	var state string
	switch health.State {
	case ingest.HealthReading:
		state = "Reading the current document in the background."
	case ingest.HealthSaved:
		state = "New content saved to encrypted memory."
	case ingest.HealthUnchanged:
		state = "Readable content is unchanged; it is already saved."
	case ingest.HealthExcluded:
		state = "The current context is excluded by capture scope or privacy policy."
	case ingest.HealthIdle:
		state = "Capture is resting while " + thisDevice + " is idle; it resumes when you return."
	case ingest.HealthPermission:
		state = "Accessibility permission is required."
		if detail := nativeReadIssueCopy(health.ReadIssue); detail != "" {
			state = detail
		}
	case ingest.HealthBrowserUnavailable:
		state = "The browser has not exposed a readable active page. Blur will retry automatically."
	case ingest.HealthDomainUnknown:
		state = "The active website could not be verified, so domain rules prevented capture."
	case ingest.HealthEmpty:
		state = "The focused app exposed no readable text. Blur will retry automatically."
	case ingest.HealthStorageError:
		state = "Content could not be saved to encrypted storage."
	case ingest.HealthTimedOut:
		state = "The focused app did not respond within the capture budget. Blur will retry."
	case ingest.HealthUnavailable:
		state = "The focused content is temporarily unavailable. Blur will retry."
		if detail := nativeReadIssueCopy(health.ReadIssue); detail != "" {
			state = detail
		}
	default:
		state = "Waiting for the first capture attempt."
	}
	if health.State == ingest.HealthExcluded && health.LastProblemAt.After(health.LastReadableAt) && health.LastProblem != "" {
		issue := health
		issue.State = health.LastProblem
		issue.LastReadableAt = time.Time{}
		issue.LastProblemAt = time.Time{}
		issue.LastReadablePartial = false
		state += " Last capture problem: " + strings.TrimSpace(captureHealthCopy(issue, true))
	}
	if health.LastReadablePartial {
		state += " The last readable content was only partially captured; uncaptured text is not searchable."
	}
	if !health.LastReadableAt.IsZero() {
		state += " Last readable content: " + health.LastReadableAt.In(time.Local).Format("Mon 15:04") + "."
	}
	return state
}

func captureHealthTextStyle(health ingest.Health, running bool) string {
	if !running {
		if health.LastReadablePartial {
			return modern.Warning
		}
		return modern.MutedText
	}
	if health.LastReadablePartial {
		return modern.Warning
	}
	switch health.State {
	case ingest.HealthPermission, ingest.HealthStorageError:
		return modern.Error
	case ingest.HealthBrowserUnavailable, ingest.HealthDomainUnknown, ingest.HealthEmpty, ingest.HealthTimedOut, ingest.HealthUnavailable:
		return modern.Warning
	case ingest.HealthExcluded:
		if health.LastProblemAt.After(health.LastReadableAt) && health.LastProblem != "" {
			return modern.Warning
		}
	}
	return modern.MutedText
}

func (d *desktop) captureHealthView() ui.View {
	if d.client == nil || d.capture == nil {
		return nil
	}
	health := d.client.CaptureHealth()
	running := d.capture.Running()
	return wrapLabel(captureHealthCopy(health, running)).Style(captureHealthTextStyle(health, running))
}
