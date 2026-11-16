package capture

import (
	"context"
	"errors"
	"testing"

	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/fake"
)

type idleCaptureSource struct {
	*fake.CaptureSource
	idle bool
}

func (s *idleCaptureSource) UserIdle(context.Context) (bool, error) { return s.idle, nil }

func TestIdleCaptureDoesNotProbeOrReadApplicationContent(t *testing.T) {
	source := &idleCaptureSource{CaptureSource: &fake.CaptureSource{Report: available()}, idle: true}
	reads := 0
	pipeline := Pipeline{Source: source, Limits: DefaultLimits(), Read: reader("not read", &reads)}
	_, err := pipeline.Capture(context.Background())
	var typed *CaptureError
	if !errors.As(err, &typed) || typed.Code != UserIdle || reads != 0 || source.Calls != 0 {
		t.Fatalf("idle capture reached application data: err=%v reads=%d calls=%d", err, reads, source.Calls)
	}
}

func TestEmptyAccessibilityFrameDoesNotBecomeSavedEvidence(t *testing.T) {
	reads := 0
	pipeline := Pipeline{Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "test", AppID: "editor"}}, Limits: DefaultLimits(), Read: reader("", &reads)}
	_, err := pipeline.Capture(context.Background())
	var typed *CaptureError
	if !errors.As(err, &typed) || typed.Code != NoContent {
		t.Fatalf("empty frame accepted: %v", err)
	}
}
