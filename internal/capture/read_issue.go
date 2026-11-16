package capture

import (
	"context"
	"errors"
)

type ReadStage string

const (
	ReadStageSetup            ReadStage = "setup"
	ReadStageApplication      ReadStage = "application"
	ReadStageWindow           ReadStage = "window"
	ReadStageRole             ReadStage = "role"
	ReadStageSensitivity      ReadStage = "sensitivity"
	ReadStageChildren         ReadStage = "children"
	ReadStageText             ReadStage = "text"
	ReadStageRange            ReadStage = "range"
	ReadStageCapability       ReadStage = "capability"
	ReadStagePresence         ReadStage = "presence"
	ReadStageProbe            ReadStage = "probe"
	ReadStageBrowserPage      ReadStage = "browser_page"
	ReadStageBrowserDomain    ReadStage = "browser_domain"
	ReadStageStructure        ReadStage = "structure"
	ReadStageValidation       ReadStage = "validation"
	ReadStageScreenPermission ReadStage = "screen_permission"
	ReadStageScreenText       ReadStage = "screen_text"
	ReadStageScreenVisibility ReadStage = "screen_visibility"
)

type ReadIssue struct {
	Stage  ReadStage
	Status int32
}

func (issue ReadIssue) Valid() bool {
	switch issue.Stage {
	case ReadStageScreenPermission, ReadStageScreenText, ReadStageScreenVisibility, ReadStageCapability, ReadStagePresence, ReadStageProbe, ReadStageBrowserPage, ReadStageBrowserDomain, ReadStageStructure, ReadStageValidation:
		return issue.Status == 0
	case ReadStageSetup, ReadStageApplication, ReadStageWindow, ReadStageRole, ReadStageSensitivity, ReadStageChildren, ReadStageText, ReadStageRange:
		return issue.Status == 0 || issue.Status >= -25215 && issue.Status <= -25200
	default:
		return false
	}
}

type nativeReadError struct {
	issue ReadIssue
	cause error
}

func (*nativeReadError) Error() string   { return "capture: native accessibility read failed" }
func (e *nativeReadError) Unwrap() error { return e.cause }

func contextualReadError(stage ReadStage, err error) error {
	if err == nil || ReadIssueForError(err).Valid() || !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		return err
	}
	issue := ReadIssue{Stage: stage}
	if !issue.Valid() {
		return err
	}
	return &nativeReadError{issue: issue, cause: err}
}

func NativeReadError(stage ReadStage, status int32) error {
	issue := ReadIssue{Stage: stage, Status: status}
	if !issue.Valid() {
		issue = ReadIssue{}
	}
	return &nativeReadError{issue: issue}
}

func ReadIssueForError(err error) ReadIssue {
	var native *nativeReadError
	if errors.As(err, &native) && native != nil && native.issue.Valid() {
		return native.issue
	}
	return ReadIssue{}
}
