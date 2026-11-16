package capture

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestReadIssueAcceptsOnlyKnownStagesAndNativeStatusRange(t *testing.T) {
	for _, stage := range []ReadStage{ReadStageSetup, ReadStageApplication, ReadStageWindow, ReadStageRole, ReadStageSensitivity, ReadStageChildren, ReadStageText, ReadStageRange} {
		for _, status := range []int32{0, -25200, -25204, -25211, -25215} {
			if !(ReadIssue{Stage: stage, Status: status}).Valid() {
				t.Fatal("known native issue rejected", stage, status)
			}
		}
	}
	for _, issue := range []ReadIssue{{}, {Stage: "private window title", Status: -25204}, {Stage: ReadStageText, Status: 1}, {Stage: ReadStageText, Status: -25199}, {Stage: ReadStageText, Status: -25216}} {
		if issue.Valid() {
			t.Fatal("untrusted read issue accepted", issue)
		}
		if got := ReadIssueForError(NativeReadError(issue.Stage, issue.Status)); got != (ReadIssue{}) {
			t.Fatal("invalid diagnostic escaped", got)
		}
	}
}

type unreadableErrorText struct{}

func (unreadableErrorText) Error() string { panic("backend error text must never be inspected") }

func TestReadIssueUnwrapsOnlyTypedNativeIssueWithoutReadingBackendText(t *testing.T) {
	want := ReadIssue{Stage: ReadStageWindow, Status: -25204}
	err := errors.Join(unreadableErrorText{}, fmt.Errorf("private page title: %w", NativeReadError(want.Stage, want.Status)))
	if got := ReadIssueForError(err); got != want {
		t.Fatal("typed cause lost through wrappers", got)
	}
	if got := ReadIssueForError(errors.New("window AXError -25204 private page title")); got != (ReadIssue{}) {
		t.Fatal("backend text became a diagnostic", got)
	}
	if got := ReadIssueForError(nil); got != (ReadIssue{}) {
		t.Fatal(got)
	}
}

func TestCaptureErrorKeepsPublicMessageSanitizedAndNativeCauseAvailable(t *testing.T) {
	err := captureError(ReadFailed, fmt.Errorf("private terminal command: %w", NativeReadError(ReadStageRange, -25204)))
	if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "terminal") || err.Error() != "capture: read_failed" {
		t.Fatal("public capture error leaked backend context", err)
	}
	if got := ReadIssueForError(err); got != (ReadIssue{Stage: ReadStageRange, Status: -25204}) {
		t.Fatal("native cause not retained", got)
	}
	if text := NativeReadError("private title", 12345).Error(); strings.Contains(text, "private") || strings.Contains(text, "12345") {
		t.Fatal("invalid issue leaked", text)
	}
}
