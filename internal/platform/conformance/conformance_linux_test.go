//go:build linux

package conformance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/linux"
)

// The Linux adapter is implemented. Without an AT-SPI bus (CI runners and
// headless hosts) it must report the service as temporarily unresponsive and
// refuse capture with that same report; with a bus it reports available and
// the private-bus integration fixture covers the rest.
func TestLinuxAdapterReportsATSPIStateAndRefusesCaptureWithoutBus(t *testing.T) {
	adapter := linux.New()
	t.Cleanup(func() { _ = adapter.Close() })

	report, err := adapter.Capability(context.Background())
	if err != nil {
		t.Fatalf("Capability() error = %v", err)
	}
	if err := report.Validate(); err != nil {
		t.Fatalf("Capability() returned invalid report: %v", err)
	}
	if report.ID != "capture" || report.Evidence != "at_spi2" {
		t.Fatalf("Capability() = %#v, want capture evidence at_spi2", report)
	}
	switch {
	case report.State == platform.Available && report.Reason == "available":
		return
	case report.State == platform.TemporarilyUnresponsive && report.Reason == "service_unavailable":
	default:
		t.Fatalf("Capability() = %#v, want available or service_unavailable", report)
	}

	for _, operation := range []struct {
		name string
		call func() error
	}{
		{"Probe", func() error { _, err := adapter.Probe(context.Background()); return err }},
		{"Next", func() error { _, err := adapter.Next(context.Background()); return err }},
	} {
		t.Run(operation.name, func(t *testing.T) {
			err := operation.call()
			var unavailable *platform.UnavailableError
			if !errors.As(err, &unavailable) {
				t.Fatalf("operation error = %v, want UnavailableError", err)
			}
			if unavailable.Report != report {
				t.Fatalf("unavailable report = %#v, want capability report %#v", unavailable.Report, report)
			}
		})
	}
}
