package conformance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nielsgroen/blur/internal/platform"
)

func assertCaptureConformance(t *testing.T, adapter platform.CaptureSource, evidence string) {
	t.Helper()

	want := platform.CapabilityReport{
		ID: "capture", State: platform.UserDisabled,
		Reason: "adapter_unimplemented", Evidence: evidence,
	}
	got, err := adapter.Capability(context.Background())
	if err != nil {
		t.Fatalf("Capability() error = %v", err)
	}
	if got != want {
		t.Fatalf("Capability() = %#v, want %#v", got, want)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Capability() returned invalid report: %v", err)
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
			if unavailable.Report != want {
				t.Fatalf("unavailable report = %#v, want %#v", unavailable.Report, want)
			}
		})
	}
}
