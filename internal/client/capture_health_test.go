package client

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-gui/goui/theme/modern"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/ingest"
)

func TestCaptureHealthKeepsUnresolvedProblemWhenBlurItselfIsExcluded(t *testing.T) {
	c := NewClient(&testData{})
	now := time.Now().UTC()
	c.setCaptureHealth(ingest.Health{State: ingest.HealthBrowserUnavailable, AttemptedAt: now, LastProblem: ingest.HealthBrowserUnavailable, LastProblemAt: now})
	c.setCaptureHealth(ingest.Health{State: ingest.HealthExcluded, AttemptedAt: now.Add(time.Second)})
	if got := captureIssueForHealth(c.CaptureHealth()); got != string(ingest.HealthBrowserUnavailable) {
		t.Fatalf("problem hidden by excluded UI: %q", got)
	}
	copy := captureHealthCopy(c.CaptureHealth(), true)
	if !strings.Contains(copy, "excluded") || !strings.Contains(copy, "readable active page") {
		t.Fatalf("coverage unclear: %s", copy)
	}
	c.setCaptureHealth(ingest.Health{State: ingest.HealthSaved, AttemptedAt: now.Add(2 * time.Second), LastReadableAt: now.Add(2 * time.Second), LastSavedAt: now.Add(2 * time.Second)})
	if issue := captureIssueForHealth(c.CaptureHealth()); issue != "" {
		t.Fatalf("resolved gap remains: %s", issue)
	}
}

func TestCaptureHealthRejectsBackendTextAndExplainsEmptyRecall(t *testing.T) {
	c := NewClient(&testData{})
	c.setCaptureHealth(ingest.Health{State: "error: private page title", AttemptedAt: time.Now()})
	if c.CaptureHealth().State != "" {
		t.Fatal("backend text escaped into capture health")
	}
	text := assistant.EmptyAnswerText(assistant.Plan{}, assistant.Diagnosis{CaptureIssue: string(ingest.HealthDomainUnknown)})
	if !strings.Contains(text, "verify the active website") || !strings.Contains(text, "no saved sources") {
		t.Fatalf("missing capture diagnosis: %s", text)
	}
	if text := captureHealthCopy(ingest.Health{State: ingest.HealthIdle}, true); !strings.Contains(text, "idle") {
		t.Fatalf("idle state hidden: %s", text)
	}
}

func TestCaptureIndicatorRequiresReadableContentAndExcludesBlur(t *testing.T) {
	for _, state := range []ingest.HealthState{ingest.HealthIdle, ingest.HealthExcluded, ingest.HealthBrowserUnavailable, ingest.HealthPermission} {
		if captureHealthActive(ingest.Health{State: state}) {
			t.Fatalf("unreadable/idle capture showed active: %s", state)
		}
	}
	if !captureHealthActive(ingest.Health{State: ingest.HealthSaved}) || captureIndicatorPolicyActive(true, true, "com.hazcod.blur", nil, nil, false) {
		t.Fatal("capture indicator does not match capture boundary")
	}
}

func TestDocumentContinuationShowsReadingWithoutClaimingItIsSaved(t *testing.T) {
	health := ingest.Health{State: ingest.HealthReading}
	if !captureHealthActive(health) {
		t.Fatal("active document read shown as stopped")
	}
	if text := captureHealthCopy(health, true); !strings.Contains(text, "Reading") || strings.Contains(text, "saved") {
		t.Fatal("unfinished document shown as saved", text)
	}
	if text := captureMenuDetail(true, true, "editor.app", nil, nil, false, health); text != "reading the current document" {
		t.Fatal("document progress hidden", text)
	}
}

func TestPartialCaptureIsDisclosedInHealthAndEmptyRecall(t *testing.T) {
	c := NewClient(&testData{})
	defer c.Close()
	now := time.Now().UTC()
	c.setCaptureHealth(ingest.Health{State: ingest.HealthSaved, AttemptedAt: now, LastReadableAt: now, LastSavedAt: now, LastReadablePartial: true})
	c.setCaptureHealth(ingest.Health{State: ingest.HealthExcluded, AttemptedAt: now.Add(time.Second)})
	h := c.CaptureHealth()
	if !h.LastReadablePartial || captureIssueForHealth(h) != "partial_content" {
		t.Fatal("partial coverage disappeared", h)
	}
	if copy := captureHealthCopy(h, true); !strings.Contains(copy, "only partially captured") || strings.Contains(copy, "Waiting for the first") {
		t.Fatal("partial capture status was misleading", copy)
	}
	answer := assistant.EmptyAnswerText(assistant.Plan{}, assistant.Diagnosis{CaptureIssue: captureIssueForHealth(h)})
	if !strings.Contains(answer, "partially captured") || strings.Contains(answer, "no saved sources") {
		t.Fatal("empty recall hid partial content", answer)
	}
}

func TestNativeCaptureHealthExplainsStageAndStatusWithoutBlamingPermission(t *testing.T) {
	for _, tc := range []struct {
		issue capture.ReadIssue
		want  string
	}{
		{capture.ReadIssue{Stage: capture.ReadStageSetup, Status: 0}, "reader"},
		{capture.ReadIssue{Stage: capture.ReadStageApplication, Status: -25204}, "app"},
		{capture.ReadIssue{Stage: capture.ReadStageWindow, Status: -25204}, "window"},
		{capture.ReadIssue{Stage: capture.ReadStageRole, Status: -25204}, "role"},
		{capture.ReadIssue{Stage: capture.ReadStageSensitivity, Status: -25204}, "privacy"},
		{capture.ReadIssue{Stage: capture.ReadStageChildren, Status: -25204}, "elements"},
		{capture.ReadIssue{Stage: capture.ReadStageText, Status: -25204}, "text"},
		{capture.ReadIssue{Stage: capture.ReadStageRange, Status: -25204}, "range"},
		{capture.ReadIssue{Stage: capture.ReadStageCapability}, "capability"},
		{capture.ReadIssue{Stage: capture.ReadStagePresence}, "session"},
		{capture.ReadIssue{Stage: capture.ReadStageProbe}, "identity"},
		{capture.ReadIssue{Stage: capture.ReadStageBrowserPage}, "page"},
		{capture.ReadIssue{Stage: capture.ReadStageBrowserDomain}, "domain"},
		{capture.ReadIssue{Stage: capture.ReadStageStructure}, "structure"},
		{capture.ReadIssue{Stage: capture.ReadStageValidation}, "checks"},
	} {
		copy := captureHealthCopy(ingest.Health{State: ingest.HealthUnavailable, ReadIssue: tc.issue}, true)
		if !strings.Contains(strings.ToLower(copy), tc.want) {
			t.Fatal("failed read stage hidden", tc.issue, copy)
		}
		if tc.issue.Status == -25204 && (!strings.Contains(copy, "-25204") || !strings.Contains(copy, "CannotComplete") || strings.Contains(strings.ToLower(copy), "permission")) {
			t.Fatal("retryable AX failure was hidden or blamed on access", copy)
		}
	}
	copy := captureHealthCopy(ingest.Health{State: ingest.HealthUnavailable, ReadIssue: capture.ReadIssue{Stage: capture.ReadStageWindow, Status: 0}}, true)
	if !strings.Contains(copy, "focused window") || !strings.Contains(strings.ToLower(copy), "focus") {
		t.Fatal("missing window has no guidance", copy)
	}
	copy = captureHealthCopy(ingest.Health{State: ingest.HealthPermission, ReadIssue: capture.ReadIssue{Stage: capture.ReadStageApplication, Status: -25211}}, true)
	for _, want := range []string{"-25211", "APIDisabled", "System Settings", "Accessibility"} {
		if !strings.Contains(copy, want) {
			t.Fatal("native access error lacks actionable guidance", copy)
		}
	}
}

func TestCaptureHealthStylesActionableProblemsBySeverity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		health  ingest.Health
		running bool
		want    string
	}{
		{"permission", ingest.Health{State: ingest.HealthPermission}, true, modern.Error},
		{"storage", ingest.Health{State: ingest.HealthStorageError}, true, modern.Error},
		{"timeout", ingest.Health{State: ingest.HealthTimedOut}, true, modern.Warning},
		{"unavailable", ingest.Health{State: ingest.HealthUnavailable}, true, modern.Warning},
		{"empty", ingest.Health{State: ingest.HealthEmpty}, true, modern.Warning},
		{"unresolved problem under blur", ingest.Health{State: ingest.HealthExcluded, LastProblem: ingest.HealthTimedOut, LastProblemAt: time.Now().Add(time.Second)}, true, modern.Warning},
		{"partial while paused", ingest.Health{State: ingest.HealthSaved, LastReadablePartial: true}, false, modern.Warning},
		{"reading", ingest.Health{State: ingest.HealthReading}, true, modern.MutedText},
		{"excluded", ingest.Health{State: ingest.HealthExcluded}, true, modern.MutedText},
		{"paused", ingest.Health{State: ingest.HealthIdle}, false, modern.MutedText},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := captureHealthTextStyle(tc.health, tc.running); got != tc.want {
				t.Fatalf("capture health style = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClientCaptureHealthRetainsDiagnosticForOwnUIAndClearsOnSuccess(t *testing.T) {
	c := NewClient(&testData{})
	defer c.Close()
	now := time.Now().UTC()
	issue := capture.ReadIssue{Stage: capture.ReadStageWindow, Status: -25204}
	c.setCaptureHealth(ingest.Health{State: ingest.HealthUnavailable, AttemptedAt: now, LastProblem: ingest.HealthUnavailable, LastProblemAt: now, ReadIssue: issue})
	c.setCaptureHealth(ingest.Health{State: ingest.HealthExcluded, AttemptedAt: now.Add(time.Second)})
	if h := c.CaptureHealth(); h.ReadIssue != issue || !strings.Contains(captureHealthCopy(h, true), "-25204") {
		t.Fatal("own UI erased diagnostic", h)
	}
	c.setCaptureHealth(ingest.Health{State: ingest.HealthSaved, AttemptedAt: now.Add(2 * time.Second), LastReadableAt: now.Add(2 * time.Second), ReadIssue: issue})
	if h := c.CaptureHealth(); h.ReadIssue != (capture.ReadIssue{}) || strings.Contains(captureHealthCopy(h, true), "-25204") {
		t.Fatal("successful read kept stale diagnostic", h)
	}
}

func TestCaptureReadDiagnosticNeverRendersUnknownStageOrNativeCode(t *testing.T) {
	for _, issue := range []capture.ReadIssue{{Stage: "private terminal command", Status: -25204}, {Stage: capture.ReadStageText, Status: 12345}} {
		h := ingest.Health{State: ingest.HealthUnavailable, AttemptedAt: time.Now(), ReadIssue: issue}
		c := NewClient(&testData{})
		c.setCaptureHealth(h)
		if c.CaptureHealth().State != "" {
			t.Fatal("invalid native issue admitted", c.CaptureHealth())
		}
		c.Close()
		for _, text := range []string{captureHealthCopy(h, true), captureMenuDetail(true, true, "terminal.app", nil, nil, false, h)} {
			if strings.Contains(text, "private") || strings.Contains(text, "12345") || strings.Contains(text, "-25204") {
				t.Fatal("invalid diagnostic rendered", text)
			}
		}
	}
}

func TestVisualCaptureHealthDistinguishesScreenPermissionAndDegradedCoverage(t *testing.T) {
	for _, tc := range []struct {
		stage capture.ReadStage
		state ingest.HealthState
		want  string
	}{
		{capture.ReadStageScreenPermission, ingest.HealthPermission, "Screen Recording"},
		{capture.ReadStageScreenText, ingest.HealthUnavailable, "on-device visible-text reader"},
		{capture.ReadStageScreenVisibility, ingest.HealthUnavailable, "fully visible"},
	} {
		health := ingest.Health{State: tc.state, ReadIssue: capture.ReadIssue{Stage: tc.stage}, AttemptedAt: time.Now()}
		got := captureHealthCopy(health, true)
		if !strings.Contains(got, tc.want) || strings.Contains(got, "Accessibility permission") || strings.Contains(got, "no readable text") {
			t.Fatalf("misleading visual capture status: %s", got)
		}
	}
}
