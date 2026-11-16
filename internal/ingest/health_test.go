package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/platform"
)

func TestPeriodicRefreshCapturesContentThatLoadsWithoutFocusEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	source := &eventSource{events: make(chan platform.CaptureEvent)}
	sink := &recordingSink{}
	observation, _ := capture.SanitizeObservation("editor", "", "Content loaded after focus", 100)
	var calls atomic.Int32
	service, err := newService(source, captureFunc(func(context.Context) (capture.SanitizedObservation, error) {
		if calls.Add(1) == 1 {
			return capture.SanitizedObservation{}, &capture.CaptureError{Code: capture.NoContent}
		}
		return observation, nil
	}), sink, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	service.refreshInterval = 5 * time.Millisecond
	health := make(chan Health, 4)
	service.SetHealthNotify(func(sample Health) {
		health <- sample
		if sample.State == HealthSaved {
			cancel()
		}
	})
	err = service.Run(ctx)
	if !errors.Is(err, context.Canceled) || calls.Load() != 2 || sink.writes != 1 {
		t.Fatalf("periodic capture calls=%d writes=%d err=%v", calls.Load(), sink.writes, err)
	}
	first, second := <-health, <-health
	if first.State != HealthEmpty || !first.LastReadableAt.IsZero() || second.State != HealthSaved || second.LastSavedAt.IsZero() || !second.LastReadableAt.After(second.LastProblemAt) || second.Stats.Persisted != 1 {
		t.Fatalf("coverage first=%+v second=%+v", first, second)
	}
}

func TestCaptureHealthClassifiesRecoverableGapsWithoutSavingEmptySources(t *testing.T) {
	for _, tc := range []struct {
		code  capture.ErrorCode
		state HealthState
	}{
		{capture.PermissionRequired, HealthPermission}, {capture.Excluded, HealthExcluded}, {capture.BrowserUnavailable, HealthBrowserUnavailable}, {capture.DomainUnknown, HealthDomainUnknown}, {capture.NoContent, HealthEmpty}, {capture.UserIdle, HealthIdle}, {capture.TimedOut, HealthTimedOut},
	} {
		t.Run(string(tc.code), func(t *testing.T) {
			sink := &recordingSink{}
			service, _ := newService(&eventSource{}, captureFunc(func(context.Context) (capture.SanitizedObservation, error) {
				return capture.SanitizedObservation{}, &capture.CaptureError{Code: tc.code}
			}), sink, slog.New(slog.NewTextHandler(io.Discard, nil)))
			var sample Health
			service.SetHealthNotify(func(value Health) { sample = value })
			if err := service.captureCurrent(context.Background()); err != nil {
				t.Fatal(err)
			}
			if sample.State != tc.state || !sample.Valid() || sink.writes != 0 || sample.Stats.Skipped != 1 {
				t.Fatalf("coverage=%+v writes=%d", sample, sink.writes)
			}
		})
	}
}

func TestUnreadableOrExcludedContextStopsAttributingTimeToPreviousSource(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	observation, _ := capture.SanitizeObservation("editor", "", "saved source", 100)
	if err := store.PutObservation(ctx, observation); err != nil {
		t.Fatal(err)
	}
	var calls int
	service, err := newServiceWithActivity(&eventSource{}, captureFunc(func(context.Context) (capture.SanitizedObservation, error) {
		calls++
		if calls > 1 {
			return capture.SanitizedObservation{}, &capture.CaptureError{Code: capture.UserIdle}
		}
		return observation, nil
	}), store, activity.New(activity.Config{MaxGap: time.Hour}), store, time.Now().Add(-time.Second), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.captureCurrent(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.captureCurrent(ctx); err != nil {
		t.Fatal(err)
	}
	before := len(service.activity.Segments())
	if err := service.captureCurrent(ctx); err != nil {
		t.Fatal(err)
	}
	if before != 1 || len(service.activity.Segments()) != before {
		t.Fatal("idle refresh kept attributing time to the old source")
	}
}

func TestCaptureHealthUnchangedContentPreservesLastSavedTime(t *testing.T) {
	observation, _ := capture.SanitizeObservation("editor", "", "saved source", 100)
	service, _ := newService(&eventSource{}, captureFunc(func(context.Context) (capture.SanitizedObservation, error) { return observation, nil }), &recordingSink{}, nil)
	var samples []Health
	service.SetHealthNotify(func(value Health) { samples = append(samples, value) })
	if err := service.captureCurrent(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.captureCurrent(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 || samples[1].State != HealthUnchanged || !samples[1].LastSavedAt.Equal(samples[0].LastSavedAt) || samples[1].Stats.Duplicates != 1 {
		t.Fatalf("unchanged coverage=%+v", samples)
	}
}

func TestCaptureHealthReportsPartialContentUntilNextCompleteRead(t *testing.T) {
	partial, err := capture.SanitizeObservation("editor.app", "", "Visible\npassword:x", len("Visible\npassword:x"))
	if err != nil || !partial.Truncated() {
		t.Fatal("invalid partial fixture", err)
	}
	complete, err := capture.SanitizeObservation("editor.app", "", "Complete visible source", 100)
	if err != nil {
		t.Fatal(err)
	}
	current := partial
	service, _ := newService(&eventSource{}, captureFunc(func(context.Context) (capture.SanitizedObservation, error) { return current, nil }), &recordingSink{}, nil)
	var samples []Health
	service.SetHealthNotify(func(h Health) { samples = append(samples, h) })
	for n := 0; n < 2; n++ {
		if err := service.captureCurrent(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	current = complete
	if err := service.captureCurrent(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(samples) != 3 || !samples[0].LastReadablePartial || !samples[1].LastReadablePartial || samples[2].LastReadablePartial {
		t.Fatal("partial coverage state was lost or never cleared", samples)
	}
}

func TestHealthAcceptsOnlyWhitelistedNativeReadDiagnostics(t *testing.T) {
	now := time.Now().UTC()
	for _, issue := range []capture.ReadIssue{{}, {Stage: capture.ReadStageWindow, Status: -25204}, {Stage: capture.ReadStageText, Status: 0}} {
		if !(Health{State: HealthUnavailable, AttemptedAt: now, ReadIssue: issue}).Valid() {
			t.Fatal("safe native diagnostic rejected", issue)
		}
	}
	for _, issue := range []capture.ReadIssue{{Stage: "private terminal title", Status: -25204}, {Stage: capture.ReadStageText, Status: -12345}, {Status: -25204}} {
		if (Health{State: HealthUnavailable, AttemptedAt: now, ReadIssue: issue}).Valid() {
			t.Fatal("backend text or arbitrary status admitted", issue)
		}
	}
}

func TestCaptureHealthRetainsTypedIssueAcrossExclusionAndClearsOnReadableContent(t *testing.T) {
	observation, err := capture.SanitizeObservation("terminal.app", "", "synthetic saved output", 100)
	if err != nil {
		t.Fatal(err)
	}
	native := capture.ReadIssue{Stage: capture.ReadStageWindow, Status: -25204}
	failure := fmt.Errorf("private terminal command: %w", errors.Join(&capture.CaptureError{Code: capture.ReadFailed}, capture.NativeReadError(native.Stage, native.Status)))
	currentErr := failure
	service, err := newService(&eventSource{}, captureFunc(func(context.Context) (capture.SanitizedObservation, error) {
		if currentErr != nil {
			return capture.SanitizedObservation{}, currentErr
		}
		return observation, nil
	}), &recordingSink{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	var samples []Health
	service.SetHealthNotify(func(h Health) { samples = append(samples, h) })
	for _, next := range []error{failure, &capture.CaptureError{Code: capture.Excluded}, nil, failure, nil, errors.Join(&capture.CaptureError{Code: capture.ReadFailed}, errors.New("private backend string AXError -25204"))} {
		currentErr = next
		if err := service.captureCurrent(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(samples) != 6 || samples[0].ReadIssue != native || samples[1].ReadIssue != native || samples[2].ReadIssue != (capture.ReadIssue{}) || samples[3].ReadIssue != native || samples[4].ReadIssue != (capture.ReadIssue{}) || samples[5].ReadIssue != (capture.ReadIssue{}) {
		t.Fatal("native issue was lost, persisted after success, or parsed from backend text", samples)
	}
	if samples[0].State != HealthUnavailable || samples[2].State != HealthSaved || samples[4].State != HealthUnchanged {
		t.Fatal("capture outcome changed", samples)
	}
	for _, sample := range samples {
		if !sample.Valid() {
			t.Fatal("invalid health sample", sample)
		}
		data, err := json.Marshal(sample)
		if err != nil || strings.Contains(string(data), "private") || strings.Contains(string(data), "terminal.app") {
			t.Fatal("health leaked backend context", string(data), err)
		}
	}
}

func TestNativeAPIDisabledBecomesPermissionHealthWithoutMisclassifyingCannotComplete(t *testing.T) {
	for _, tc := range []struct {
		status int32
		want   HealthState
	}{{-25211, HealthPermission}, {-25204, HealthUnavailable}} {
		service, _ := newService(&eventSource{}, captureFunc(func(context.Context) (capture.SanitizedObservation, error) {
			return capture.SanitizedObservation{}, errors.Join(&capture.CaptureError{Code: capture.ReadFailed}, capture.NativeReadError(capture.ReadStageApplication, tc.status))
		}), &recordingSink{}, nil)
		if err := service.captureCurrent(context.Background()); err != nil {
			t.Fatal(err)
		}
		if service.health.State != tc.want {
			t.Fatal("native status misclassified", tc.status, service.health.State)
		}
	}
}

func TestVisualCaptureHealthCodesAreRecoverable(t *testing.T) {
	for _, tc := range []struct {
		code  capture.ErrorCode
		state HealthState
	}{
		{capture.ScreenPermissionRequired, HealthPermission}, {capture.ScreenTextUnavailable, HealthUnavailable}, {capture.ScreenNotVisible, HealthUnavailable},
	} {
		service, _ := newService(&eventSource{}, captureFunc(func(context.Context) (capture.SanitizedObservation, error) {
			return capture.SanitizedObservation{}, &capture.CaptureError{Code: tc.code}
		}), &recordingSink{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		var sample Health
		service.SetHealthNotify(func(h Health) { sample = h })
		if err := service.captureCurrent(context.Background()); err != nil || sample.State != tc.state || sample.Stats.Skipped != 1 {
			t.Fatalf("visual failure not recoverable: code=%s health=%s err=%v", tc.code, sample.State, err)
		}
	}
}

type policyRejectedSink struct{ recordingSink }

func (*policyRejectedSink) PutObservation(context.Context, capture.SanitizedObservation) error {
	return &capture.CaptureError{Code: capture.Excluded}
}
func TestLateCapturePolicyRejectionIsExcludedNotStorageFailure(t *testing.T) {
	observation, err := capture.SanitizeObservation("dev.herdr.app", "", "Synthetic", 1024)
	if err != nil {
		t.Fatal(err)
	}
	service, _ := newService(&eventSource{}, captureFunc(func(context.Context) (capture.SanitizedObservation, error) { return observation, nil }), &policyRejectedSink{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var sample Health
	service.SetHealthNotify(func(h Health) { sample = h })
	if err := service.captureCurrent(context.Background()); err != nil || sample.State != HealthExcluded || sample.Stats.Skipped != 1 || sample.Stats.Persisted != 0 {
		t.Fatalf("policy rejection misclassified: health=%s err=%v", sample.State, err)
	}
}
