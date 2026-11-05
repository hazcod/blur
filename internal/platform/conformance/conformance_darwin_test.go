//go:build darwin

package conformance_test

import (
	"testing"

	"github.com/nielsgroen/blur/internal/platform/darwin"
)

func TestDarwinAdapterReportsEvidenceAndRefusesUnimplementedCapture(t *testing.T) {
	assertCaptureConformance(t, darwin.New(), "axui_element")
}
