package capture

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/platform/darwin"
)

type syntheticVisualReader struct {
	window          darwin.ScreenTextWindow
	text            string
	topErr, readErr error
	reads           int
	change          func(*darwin.ScreenTextResult)
}

func (s *syntheticVisualReader) TopmostWindow(context.Context) (darwin.ScreenTextWindow, error) {
	return s.window, s.topErr
}
func (s *syntheticVisualReader) RecognizeWindow(_ context.Context, r darwin.ScreenTextRequest) (darwin.ScreenTextResult, error) {
	s.reads++
	if r.PID != s.window.PID || r.WindowID != s.window.WindowID || r.LeaseToken != s.window.LeaseToken || r.Timeout != MaxVisualDuration || r.MaxTextBytes != 4096 {
		return darwin.ScreenTextResult{}, errors.New("wrong bounded native request")
	}
	result := darwin.ScreenTextResult{Window: s.window, Text: s.text, CapturedAt: time.Now()}
	if s.change != nil {
		s.change(&result)
	}
	return result, s.readErr
}
func visualWindow() darwin.ScreenTextWindow {
	return darwin.ScreenTextWindow{PID: 42, WindowID: 7, BundleID: "dev.herdr.app", Title: "Synthetic canvas", TitleAvailable: true, Frame: darwin.ScreenTextRect{X: 10, Y: 10, Width: 800, Height: 600}, FullOnscreen: true, Unobscured: true, PermissionGranted: true, LeaseToken: strings.Repeat("a", 64)}
}

func TestVisualNativeReaderRequiresVerifiedWindowOnBothSidesOfRecognition(t *testing.T) {
	for _, scenario := range []string{"success", "no-permission", "obscured", "stale-before", "stale-after", "forged-result", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reader := &syntheticVisualReader{window: visualWindow(), text: "Synthetic canvas text"}
			switch scenario {
			case "no-permission":
				reader.window.PermissionGranted = false
			case "obscured":
				reader.window.Unobscured = false
			case "forged-result":
				reader.change = func(r *darwin.ScreenTextResult) { r.Window.WindowID++ }
			case "cancel":
				reader.change = func(*darwin.ScreenTextResult) { cancel() }
			}
			checks := 0
			text, _, err := readVisualText(ctx, reader, func(context.Context, darwin.ScreenTextWindow) error {
				checks++
				if scenario == "stale-before" || scenario == "stale-after" && checks == 2 {
					return captureError(Excluded, nil)
				}
				return nil
			}, "synthetic", 4096, nil)
			if scenario == "success" {
				if err != nil || text != reader.text || checks != 2 || reader.reads != 1 {
					t.Fatal("valid native flow failed", err)
				}
				return
			}
			if err == nil || text != "" {
				t.Fatal("unverified native output escaped", err)
			}
			if (scenario == "no-permission" || scenario == "obscured" || scenario == "stale-before") && reader.reads != 0 {
				t.Fatal("native screenshot began before policy proof")
			}
			if scenario == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("native cancellation detached", err)
			}
		})
	}
}

func TestVisualErrorsStayClosedAndDoNotClaimNoTextForUnavailableReaders(t *testing.T) {
	for _, tc := range []struct {
		err   error
		code  ErrorCode
		stage ReadStage
	}{
		{&darwin.ScreenTextError{Status: "permission_required"}, ScreenPermissionRequired, ReadStageScreenPermission},
		{&darwin.ScreenTextError{Status: "not_visible"}, ScreenNotVisible, ReadStageScreenVisibility},
		{&darwin.ScreenTextError{Status: "image_limit"}, ScreenNotVisible, ReadStageScreenVisibility},
		{darwin.ErrScreenTextUnavailable, ScreenTextUnavailable, ReadStageScreenText},
		{errors.New("private native backend detail"), ScreenTextUnavailable, ReadStageScreenText},
		{&darwin.ScreenTextError{Status: "no_text"}, NoContent, ""},
		{&darwin.ScreenTextError{Status: "target_changed"}, Excluded, ""},
		{&darwin.ScreenTextError{Status: "timeout"}, TimedOut, ""},
	} {
		err := visualCaptureError(tc.err)
		var classified *CaptureError
		if !errors.As(err, &classified) || classified.Code != tc.code || ReadIssueForError(err).Stage != tc.stage || strings.Contains(err.Error(), "private") {
			t.Fatal("unsafe/misleading native error", err)
		}
	}
}
