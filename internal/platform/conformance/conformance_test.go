package conformance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hazcod/blur/internal/platform"
)

func assertCaptureConformance(t *testing.T, adapter platform.CaptureSource, evidence string) {
	t.Helper()

	got, err := adapter.Capability(context.Background())
	if err != nil {
		t.Fatalf("Capability() error = %v", err)
	}
	if got.ID != "capture" || got.Evidence != evidence ||
		!(got.State == platform.UserDisabled && got.Reason == "adapter_unimplemented" ||
			got.State == platform.PermissionRequired && got.Reason == "permission_required") {
		t.Fatalf("Capability() = %#v, not a supported unimplemented/permission state", got)
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
			if unavailable.Report != got {
				t.Fatalf("unavailable report = %#v, want capability report %#v", unavailable.Report, got)
			}
		})
	}
}
