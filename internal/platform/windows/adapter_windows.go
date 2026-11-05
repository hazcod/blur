package windows

import (
	"context"

	"github.com/nielsgroen/blur/internal/platform"
)

var _ platform.CaptureSource = (*Adapter)(nil)

// Adapter reserves the Windows capture boundary for UI Automation integration.
// Native calls remain unavailable until they have reviewed fixtures.
type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func (*Adapter) Capability(ctx context.Context) (platform.CapabilityReport, error) {
	report := platform.CapabilityReport{
		ID: "capture", State: platform.UserDisabled,
		Reason: "adapter_unimplemented", Evidence: "ui_automation",
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	return report, nil
}

func (a *Adapter) Probe(context.Context) (platform.CaptureProbe, error) {
	return platform.CaptureProbe{}, a.unavailable()
}

func (a *Adapter) Next(context.Context) (platform.CaptureEvent, error) {
	return platform.CaptureEvent{}, a.unavailable()
}

func (*Adapter) unavailable() error {
	return &platform.UnavailableError{Report: platform.CapabilityReport{
		ID: "capture", State: platform.UserDisabled,
		Reason: "adapter_unimplemented", Evidence: "ui_automation",
	}}
}
