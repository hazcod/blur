//go:build windows

package conformance_test

import (
	"testing"

	"github.com/nielsgroen/blur/internal/platform/windows"
)

func TestWindowsAdapterReportsEvidenceAndRefusesUnimplementedCapture(t *testing.T) {
	assertCaptureConformance(t, windows.New(), "ui_automation")
}
