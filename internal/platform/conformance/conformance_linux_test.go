//go:build linux

package conformance_test

import (
	"testing"

	"github.com/nielsgroen/blur/internal/platform/linux"
)

func TestLinuxAdapterReportsEvidenceAndRefusesUnimplementedCapture(t *testing.T) {
	assertCaptureConformance(t, linux.New(), "at_spi2")
}
