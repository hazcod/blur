package client

import (
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/ingest"
)

func TestCaptureMenuExplainsWhyCaptureAllHasNoReadableContent(t *testing.T) {
	for _, tc := range []struct {
		name, app, want string
		state           ingest.HealthState
	}{
		{"own window", "com.hazcod.blur", "Blur’s own windows", ingest.HealthExcluded},
		{"starting", "editor.app", "waiting for readable content", ""},
		{"idle", "editor.app", "idle", ingest.HealthIdle},
		{"no text", "editor.app", "no readable text", ingest.HealthEmpty},
		{"browser", "com.apple.Safari", "browser text", ingest.HealthBrowserUnavailable},
		{"domain", "com.apple.Safari", "website could not be verified", ingest.HealthDomainUnknown},
		{"permission", "editor.app", "Accessibility", ingest.HealthPermission},
		{"save failure", "editor.app", "save failed", ingest.HealthStorageError},
		{"read failure", "editor.app", "could not be read", ingest.HealthUnavailable},
		{"interrupted capture", "editor.app", "interrupted", ingest.HealthCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := captureMenuDetail(true, true, tc.app, nil, nil, false, ingest.Health{State: tc.state})
			if !strings.Contains(got, tc.want) || strings.Contains(got, "current app is not being captured") {
				t.Fatalf("menu conflates %s with a capture exclusion: %q", tc.name, got)
			}
		})
	}
}

func TestCaptureMenuHonorsScopeWithoutConfusingItWithHealth(t *testing.T) {
	h := ingest.Health{State: ingest.HealthSaved}
	if got := captureMenuDetail(true, true, "editor.app", nil, nil, false, h); got != "" {
		t.Fatalf("capture all with a saved source reported a problem: %q", got)
	}
	if got := captureMenuDetail(true, true, "editor.app", []string{"editor.app"}, nil, false, h); !strings.Contains(got, "capture rules") {
		t.Fatalf("explicit exclusion hidden: %q", got)
	}
	if got := captureMenuDetail(true, true, "editor.app", nil, nil, true, h); !strings.Contains(got, "capture rules") {
		t.Fatalf("opt-in capture scope hidden: %q", got)
	}
	if got := captureMenuDetail(true, true, "editor.app", nil, []string{"editor.app"}, true, h); got != "" {
		t.Fatalf("included app treated as excluded: %q", got)
	}
	if got := captureMenuDetail(false, true, "editor.app", nil, nil, false, h); got != "" {
		t.Fatalf("paused menu retained a running-state detail: %q", got)
	}
}

func TestCaptureMenuShowsNativeReadFailureAndRetainsItForBlurWindows(t *testing.T) {
	now := time.Now().UTC()
	h := ingest.Health{State: ingest.HealthUnavailable, AttemptedAt: now, ReadIssue: capture.ReadIssue{Stage: capture.ReadStageWindow, Status: -25204}, LastProblem: ingest.HealthUnavailable, LastProblemAt: now}
	for _, app := range []string{"terminal.app", "com.hazcod.blur"} {
		copy := captureMenuDetail(true, true, app, nil, nil, false, h)
		for _, want := range []string{"window", "-25204", "CannotComplete"} {
			if !strings.Contains(copy, want) {
				t.Fatal("menu hides native read failure", app, copy)
			}
		}
		if strings.Contains(strings.ToLower(copy), "permission") {
			t.Fatal("CannotComplete was blamed on permission", copy)
		}
	}
	h.ReadIssue = capture.ReadIssue{Stage: capture.ReadStageApplication, Status: -25211}
	h.State = ingest.HealthPermission
	copy := captureMenuDetail(true, true, "terminal.app", nil, nil, false, h)
	if !strings.Contains(copy, "-25211") || !strings.Contains(copy, "Accessibility") {
		t.Fatal("menu hides native access failure", copy)
	}
}
