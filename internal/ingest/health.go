package ingest

import (
	"time"

	"github.com/hazcod/blur/internal/capture"
)

type HealthState string

const (
	HealthSaved              HealthState = "saved"
	HealthReading            HealthState = "reading"
	HealthUnchanged          HealthState = "unchanged"
	HealthStorageError       HealthState = "storage_error"
	HealthUnavailable        HealthState = "unavailable"
	HealthPermission         HealthState = "permission_required"
	HealthExcluded           HealthState = "excluded"
	HealthBrowserUnavailable HealthState = "browser_content_unavailable"
	HealthDomainUnknown      HealthState = "browser_domain_unknown"
	HealthEmpty              HealthState = "no_content"
	HealthTimedOut           HealthState = "timed_out"
	HealthCancelled          HealthState = "cancelled"
	HealthIdle               HealthState = "user_idle"
)

// Health is transient, content-free capture coverage. It contains no app,
// domain, window title, source identifier, captured text, or backend error.
type Health struct {
	State               HealthState
	ReadIssue           capture.ReadIssue
	AttemptedAt         time.Time
	LastReadableAt      time.Time
	LastReadablePartial bool
	LastSavedAt         time.Time
	LastProblem         HealthState
	LastProblemAt       time.Time
	Stats               Stats
}

func (h Health) Valid() bool {
	if h.ReadIssue != (capture.ReadIssue{}) && !h.ReadIssue.Valid() {
		return false
	}
	switch h.State {
	case HealthReading, HealthSaved, HealthUnchanged, HealthStorageError, HealthUnavailable, HealthPermission, HealthExcluded, HealthBrowserUnavailable, HealthDomainUnknown, HealthEmpty, HealthTimedOut, HealthCancelled, HealthIdle:
		return !h.AttemptedAt.IsZero() && (h.LastProblem == "" || healthProblem(h.LastProblem) && !h.LastProblemAt.IsZero())
	default:
		return false
	}
}

func (s *Service) SetHealthNotify(notify func(Health)) {
	if s == nil || s.started.Load() {
		return
	}
	s.onHealth = notify
}

func healthForCode(code capture.ErrorCode) HealthState {
	switch code {
	case capture.PermissionRequired, capture.ScreenPermissionRequired:
		return HealthPermission
	case capture.Excluded:
		return HealthExcluded
	case capture.BrowserUnavailable:
		return HealthBrowserUnavailable
	case capture.DomainUnknown:
		return HealthDomainUnknown
	case capture.NoContent:
		return HealthEmpty
	case capture.TimedOut:
		return HealthTimedOut
	case capture.Cancelled:
		return HealthCancelled
	case capture.UserIdle:
		return HealthIdle
	default:
		return HealthUnavailable
	}
}

func (s *Service) reportHealth(state HealthState, at time.Time, issue capture.ReadIssue) {
	s.health.State, s.health.AttemptedAt = state, at
	if state == HealthSaved || state == HealthUnchanged {
		s.health.LastReadableAt = at
		s.health.ReadIssue = capture.ReadIssue{}
	}
	if state == HealthSaved {
		s.health.LastSavedAt = at
	}
	if healthProblem(state) {
		s.health.LastProblem, s.health.LastProblemAt = state, at
		s.health.ReadIssue = capture.ReadIssue{}
		if issue.Valid() {
			s.health.ReadIssue = issue
		}
	}
	s.health.Stats = s.Stats()
	if s.onHealth != nil {
		s.onHealth(s.health)
	}
}

func healthProblem(state HealthState) bool {
	switch state {
	case HealthStorageError, HealthUnavailable, HealthPermission, HealthBrowserUnavailable, HealthDomainUnknown, HealthEmpty, HealthTimedOut:
		return true
	default:
		return false
	}
}
