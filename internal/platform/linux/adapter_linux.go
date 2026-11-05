package linux

import (
	"context"

	"github.com/nielsgroen/blur/internal/platform"
)

var _ platform.CaptureSource = (*Adapter)(nil)

// Adapter reserves the Linux capture boundary for AT-SPI2 integration.
// Native calls remain unavailable until they have reviewed fixtures.
type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func (*Adapter) Capability(ctx context.Context) (platform.CapabilityReport, error) {
	report := platform.CapabilityReport{
		ID: "capture", State: platform.UserDisabled,
		Reason: "adapter_unimplemented", Evidence: "at_spi2",
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
		Reason: "adapter_unimplemented", Evidence: "at_spi2",
	}}
}
