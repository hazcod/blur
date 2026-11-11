//go:build darwin

package conformance_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/darwin"
)

func TestDarwinAdapterReportsEvidenceAndUsesPassiveActivationWait(t *testing.T) {
	adapter := darwin.New()
	defer adapter.Close()
	report, err := adapter.Capability(context.Background())
	if err != nil || report.ID != "capture" || report.Evidence != "axui_element" || report.Validate() != nil {
		t.Fatalf("invalid capability report: %+v err=%v", report, err)
	}
	probe, err := adapter.Probe(context.Background())
	if err != nil {
		var unavailable *platform.UnavailableError
		if !errors.As(err, &unavailable) || unavailable.Report != report {
			t.Fatalf("probe error = %v, want current unavailable report", err)
		}
	} else if probe.AppID == "" || !strings.HasPrefix(probe.TargetID, "darwin:") || probe.Domain != "" {
		t.Fatalf("probe exposed invalid or excess metadata: %+v", probe)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	event, err := adapter.Next(ctx)
	if err == nil {
		if event.Kind != "frontmost_app_changed" || event.TargetID != "" {
			t.Fatalf("unexpected activation event: %+v", event)
		}
	} else if !errors.Is(err, context.DeadlineExceeded) {
		var unavailable *platform.UnavailableError
		if !errors.As(err, &unavailable) || unavailable.Report != report {
			t.Fatalf("Next error = %v, want blocking event wait or current capability error", err)
		}
	}
}
