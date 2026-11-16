package client

import (
	"errors"
	"fmt"

	"github.com/golang-gui/goui/layout"
	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/storage/durable"
)

// Keep only typed, non-content diagnostics. Underlying errors can contain
// paths, provider responses, or saved text and are never copied into the UI.
func (d *desktop) setStartupIssue(err error) {
	d.startupIssue = SetupIssueMessage(err)
	d.status = d.startupIssue
	code := "storage_unavailable"
	switch {
	case errors.Is(err, durable.ErrUnsupportedSchema):
		code = "storage_version_unsupported"
	case errors.Is(err, ErrMissingDataKey):
		code = "storage_key_missing"
	case errors.Is(err, ErrEncryptedStoreCorrupt):
		code = "storage_verification_failed"
	}
	d.startupDiagnostic = fmt.Sprintf("Blur storage startup\nIssue: %s\nSupported schema: %d\n%s", code, durable.OpenSchemaVersion, d.startupIssue)
	if stage := storageStartupStage(err); stage != "unknown" {
		d.startupDiagnostic += "\nStartup stage: " + stage
	}
	var secretErr *platform.SecretStoreError
	if errors.As(err, &secretErr) && secretErr != nil {
		d.startupDiagnostic += fmt.Sprintf("\nKeychain state: %s\nKeychain OSStatus: %d", secretErr.State, secretErr.BackendCode)
	}
}

func (d *desktop) copyStartupDiagnostic() {
	if err := copyToPasteboard(d.startupDiagnostic); err != nil {
		d.status = "Diagnostic details could not be copied. The error is still shown here."
	} else {
		d.status = "Diagnostic details copied."
	}
	d.app.RequestUpdate()
}

func (d *desktop) storageRecoveryView() ui.View {
	message := d.startupIssue
	if message == "" {
		message = SetupIssueMessage(nil)
	}
	rows := []ui.View{
		wrapLabel("Your memory could not be opened").Style(styleSectionTitle),
		wrapLabel(message),
		wrapLabel("Capture, notes, and memory search will be available after storage opens.").Style(modern.MutedText),
	}
	if d.startupDiagnostic != "" {
		rows = append(rows, hug(flatButton("Copy diagnostic details").OnClick(d.copyStartupDiagnostic)))
	}
	if d.status != "" && d.status != message {
		rows = append(rows, wrapLabel(d.status).Style(styleMeta))
	}
	return ui.VBox(rows...).Spacing(12).CrossAlign(layout.CrossStretch)
}
