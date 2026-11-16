package capture

import (
	"context"
	"errors"
	"github.com/hazcod/blur/internal/platform"
)

// DocumentFallbackAllowed retries an acquisition limitation through a wholly
// fresh ordinary pipeline, including its structural/privacy/visual admission.
// It carries no text or lease forward and never retries privacy/focus changes,
// permissions, cancellation, policy rejection or a failed encrypted write.
func DocumentFallbackAllowed(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, platform.ErrInvalidLease) || errors.Is(err, errBrowserPageChanged) || errors.Is(err, errCaptureDomainChanged) {
		return false
	}
	allowed, denied := false, false
	var inspect func(error)
	inspect = func(current error) {
		if current == nil {
			return
		}
		if ce, ok := current.(*CaptureError); ok {
			switch ce.Code {
			case NoContent, ReadFailed, BrowserUnavailable:
				allowed = true
			default:
				denied = true
			}
		}
		switch wrapped := current.(type) {
		case interface{ Unwrap() []error }:
			for _, nested := range wrapped.Unwrap() {
				inspect(nested)
			}
		case interface{ Unwrap() error }:
			inspect(wrapped.Unwrap())
		}
	}
	inspect(err)
	return allowed && !denied
}
