//go:build windows

package conformance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/windows"
)

func TestWindowsAdapterReportsImplementedUIAutomationCapture(t *testing.T) {
	adapter := windows.New()
	report, err := adapter.Capability(context.Background())
	if err != nil || report.ID != "capture" || report.State != platform.Available || report.Reason != "available" || report.Evidence != "ui_automation" {
		t.Fatalf("Windows capability = %#v, %v", report, err)
	}
	if err := report.Validate(); err != nil {
		t.Fatalf("Windows capability report is invalid: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.Probe(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Probe with canceled context = %v, want canceled", err)
	}
}
