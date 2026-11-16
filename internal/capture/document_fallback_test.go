package capture

import (
	"context"
	"errors"
	"github.com/hazcod/blur/internal/platform"
	"testing"
	"time"
)

func TestDocumentFallbackRetriesOnlyFreshSafeAcquisition(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"empty", captureError(NoContent, nil), true},
		{"range unsupported", captureError(ReadFailed, NativeReadError(ReadStageRange, -25205)), true},
		{"browser unavailable", captureError(BrowserUnavailable, nil), true},
		{"private", captureError(Excluded, nil), false},
		{"secure nested", captureError(ReadFailed, captureError(Excluded, nil)), false},
		{"focus", captureError(ReadFailed, errBrowserPageChanged), false},
		{"token", captureError(ReadFailed, platform.ErrInvalidLease), false},
		{"domain", captureError(ReadFailed, errCaptureDomainChanged), false},
		{"permission", captureError(PermissionRequired, nil), false},
		{"timeout", captureError(ReadFailed, context.DeadlineExceeded), false},
		{"cancel", captureError(ReadFailed, context.Canceled), false},
		{"sink", ErrCaptureOutput, false},
		{"unknown", errors.New("private unrelated diagnostic"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DocumentFallbackAllowed(tc.err); got != tc.want {
				t.Fatal(got, tc.want)
			}
		})
	}
}

type documentUnsupportedSource struct{}

func (documentUnsupportedSource) Capability(context.Context) (platform.CapabilityReport, error) {
	panic("unsupported source probed capability")
}
func (documentUnsupportedSource) Probe(context.Context) (platform.CaptureProbe, error) {
	panic("unsupported source probed")
}
func (documentUnsupportedSource) Next(context.Context) (platform.CaptureEvent, error) {
	panic("unsupported source observed")
}
func TestDocumentUnsupportedAdapterDoesNotProbeOrRead(t *testing.T) {
	p := Pipeline{Source: documentUnsupportedSource{}, Limits: DefaultLimits(), Read: func(context.Context, platform.CaptureProbe, Limits) (rawFrame, error) {
		panic("unsupported source text read")
	}}
	job, err := p.OpenDocument(context.Background(), time.Now().UTC())
	if job != nil || !errors.Is(err, ErrDocumentUnsupported) {
		t.Fatal(job, err)
	}
}
