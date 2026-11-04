// Package platform defines portable desktop capability boundaries.
package platform

import (
	"errors"
	"fmt"
)

var ErrReadLimit = errors.New("read exceeds requested limit")
var ErrInvalidLease = errors.New("target lease is invalid or stale")
var ErrInvalidCapabilityReport = errors.New("capability report is invalid")

// CapabilityState is a closed vocabulary. Unknown states fail closed.
type CapabilityState string

const (
	Available               CapabilityState = "available"
	PermissionRequired      CapabilityState = "permission_required"
	UnsupportedTarget       CapabilityState = "unsupported_target"
	SecureTarget            CapabilityState = "secure_target"
	TargetElevated          CapabilityState = "target_elevated"
	PortalMissing           CapabilityState = "portal_missing"
	TemporarilyUnresponsive CapabilityState = "temporarily_unresponsive"
	UserDisabled            CapabilityState = "user_disabled"
)

var capabilityStates = [...]CapabilityState{
	Available, PermissionRequired, UnsupportedTarget, SecureTarget,
	TargetElevated, PortalMissing, TemporarilyUnresponsive, UserDisabled,
}

func AllCapabilityStates() []CapabilityState {
	return append([]CapabilityState(nil), capabilityStates[:]...)
}

func (s CapabilityState) Valid() bool {
	for _, known := range capabilityStates {
		if s == known {
			return true
		}
	}
	return false
}

// CapabilityReport contains only a stable identifier and content-free diagnostic
// evidence. Adapters must never place captured content or environment values here.
type CapabilityReport struct {
	ID       string          `json:"id"`
	State    CapabilityState `json:"state"`
	Reason   string          `json:"reason"`
	Evidence string          `json:"evidence"`
}

func (r CapabilityReport) Validate() error {
	if !codeValid(r.ID) {
		return fmt.Errorf("invalid capability id %q", r.ID)
	}
	if !r.State.Valid() {
		return fmt.Errorf("invalid capability state %q", r.State)
	}
	if !codeValid(r.Reason) {
		return fmt.Errorf("invalid capability reason %q", r.Reason)
	}
	if r.Evidence == "" {
		return fmt.Errorf("capability evidence is empty")
	}
	return nil
}

func codeValid(code string) bool {
	if code == "" || code[0] < 'a' || code[0] > 'z' {
		return false
	}
	for i := 1; i < len(code); i++ {
		c := code[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

// UnavailableError identifies an operation denied by its current capability.
type UnavailableError struct{ Report CapabilityReport }

func (e *UnavailableError) Error() string {
	return fmt.Sprintf("capability %s unavailable: %s (%s)", e.Report.ID, e.Report.State, e.Report.Reason)
}
