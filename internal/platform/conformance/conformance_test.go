//go:build darwin

package conformance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nielsgroen/blur/internal/platform"
	"github.com/nielsgroen/blur/internal/platform/darwin"
)

func TestAdaptersReportEvidenceAndRefuseUnimplementedCapture(t *testing.T) {
	adapters := []struct {
		name     string
		evidence string
		adapter  platform.CaptureSource
	}{
		{"darwin", "axui_element", darwin.New()},
	}

	for _, test := range adapters {
		t.Run(test.name, func(t *testing.T) {
			report, err := test.adapter.Capability(context.Background())
			if err != nil {
				t.Fatalf("Capability() error = %v", err)
			}
			if report != (platform.CapabilityReport{
				ID: "capture", State: platform.UserDisabled,
				Reason: "adapter_unimplemented", Evidence: test.evidence,
			}) {
				t.Fatalf("Capability() = %#v", report)
			}
			if err := report.Validate(); err != nil {
				t.Fatalf("Capability() returned invalid report: %v", err)
			}

			for _, operation := range []struct {
				name string
				call func() error
			}{
				{"Probe", func() error { _, err := test.adapter.Probe(context.Background()); return err }},
				{"Next", func() error { _, err := test.adapter.Next(context.Background()); return err }},
			} {
				t.Run(operation.name, func(t *testing.T) {
					err := operation.call()
					var unavailable *platform.UnavailableError
					if !errors.As(err, &unavailable) {
						t.Fatalf("operation error = %v, want UnavailableError", err)
					}
					if unavailable.Report != report {
						t.Fatalf("unavailable report = %#v, want %#v", unavailable.Report, report)
					}
				})
			}
		})
	}
}
