package capture

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"

	"github.com/hazcod/blur/internal/platform/darwin"
)

// VisualTextReader provides local, in-memory recognition only. It neither grants
// capture authority nor replaces the caller's AX/privacy/focus checks.
type VisualTextReader interface {
	TopmostWindow(context.Context) (darwin.ScreenTextWindow, error)
	RecognizeWindow(context.Context, darwin.ScreenTextRequest) (darwin.ScreenTextResult, error)
}

type visualAdmission func(context.Context, func(context.Context) error) error
type visualRegionVerifier func(context.Context, darwin.ScreenTextWindow) (darwin.ScreenTextRect, error)

var visualRequestSequence atomic.Uint64

func visualCaptureError(err error) error {
	if ce := contextError(err); ce != nil {
		return ce
	}
	var classified *CaptureError
	if errors.As(err, &classified) {
		return err
	}
	var native *darwin.ScreenTextError
	if errors.As(err, &native) {
		switch native.Status {
		case "permission_required":
			return captureError(ScreenPermissionRequired, NativeReadError(ReadStageScreenPermission, 0))
		case "not_visible", "no_window", "image_limit":
			return captureError(ScreenNotVisible, NativeReadError(ReadStageScreenVisibility, 0))
		case "target_changed":
			return captureError(Excluded, nil)
		case "timeout":
			return captureError(TimedOut, context.DeadlineExceeded)
		case "cancelled":
			return captureError(Cancelled, context.Canceled)
		case "no_text":
			return captureError(NoContent, nil)
		}
	}
	return captureError(ScreenTextUnavailable, NativeReadError(ReadStageScreenText, 0))
}

// readVisualText verifies the AX/CG binding both sides of native recognition.
// The native reader independently checks visibility, foreground and lease again
// before capture, before OCR and before publishing. No image leaves that bridge.
func readVisualText(ctx context.Context, reader VisualTextReader, verify func(context.Context, darwin.ScreenTextWindow) error, requestID string, max int, admit visualAdmission) (string, bool, error) {
	if reader == nil || verify == nil {
		return "", false, darwin.ErrScreenTextUnavailable
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	window, err := reader.TopmostWindow(ctx)
	if err != nil {
		return "", false, err
	}
	if !window.PermissionGranted {
		return "", false, &darwin.ScreenTextError{Status: "permission_required"}
	}
	if !window.FullOnscreen || !window.Unobscured {
		return "", false, &darwin.ScreenTextError{Status: "not_visible"}
	}
	if err := verify(ctx, window); err != nil {
		return "", false, err
	}
	var result darwin.ScreenTextResult
	acquire := func(readCtx context.Context) error {
		if err := readCtx.Err(); err != nil {
			return err
		}
		var err error
		result, err = reader.RecognizeWindow(readCtx, darwin.ScreenTextRequest{RequestID: requestID, PID: window.PID, WindowID: window.WindowID, LeaseToken: window.LeaseToken, MaxTextBytes: max, Timeout: MaxVisualDuration})
		return err
	}
	if admit != nil {
		err = admit(ctx, acquire)
	} else {
		err = acquire(ctx)
	}
	if err != nil {
		return "", false, err
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if result.Window != window {
		return "", false, captureError(Excluded, nil)
	}
	if err := verify(ctx, window); err != nil {
		return "", false, err
	}
	if strings.TrimSpace(result.Text) == "" {
		return "", false, &darwin.ScreenTextError{Status: "no_text"}
	}
	return result.Text, result.Truncated, nil
}

// Browser recognition always carries an independently verified document-region
// authority. A missing/changed region or a reply without its exact echo is never
// allowed to silently downgrade to whole-window recognition.
func readVisualRegion(ctx context.Context, reader VisualTextReader, verify visualRegionVerifier, requestID string, max int, admit visualAdmission) (string, bool, error) {
	if reader == nil || verify == nil {
		return "", false, captureError(BrowserUnavailable, nil)
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	window, err := reader.TopmostWindow(ctx)
	if err != nil {
		return "", false, err
	}
	if !window.PermissionGranted {
		return "", false, &darwin.ScreenTextError{Status: "permission_required"}
	}
	if !window.FullOnscreen || !window.Unobscured {
		return "", false, &darwin.ScreenTextError{Status: "not_visible"}
	}
	region, err := verify(ctx, window)
	if err != nil {
		return "", false, err
	}
	if !validVisualRegion(region, window.Frame) {
		return "", false, captureError(Excluded, nil)
	}
	var result darwin.ScreenTextResult
	acquire := func(readCtx context.Context) error {
		current, err := verify(readCtx, window)
		if err != nil {
			return err
		}
		if current != region {
			return captureError(Excluded, nil)
		}
		if err := readCtx.Err(); err != nil {
			return err
		}
		result, err = reader.RecognizeWindow(readCtx, darwin.ScreenTextRequest{RequestID: requestID, PID: window.PID, WindowID: window.WindowID, LeaseToken: window.LeaseToken, MaxTextBytes: max, Timeout: MaxVisualDuration, Region: &region})
		return err
	}
	if admit != nil {
		err = admit(ctx, acquire)
	} else {
		err = acquire(ctx)
	}
	if err != nil {
		return "", false, err
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if result.Window != window || result.Region == nil || *result.Region != region {
		return "", false, captureError(Excluded, nil)
	}
	current, err := verify(ctx, window)
	if err != nil {
		return "", false, err
	}
	if current != region {
		return "", false, captureError(Excluded, nil)
	}
	if strings.TrimSpace(result.Text) == "" {
		return "", false, &darwin.ScreenTextError{Status: "no_text"}
	}
	return result.Text, true, nil // Visible-only OCR never claims full document coverage.
}
