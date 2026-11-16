package capture

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/fake"
)

type valueSupportAXNode struct {
	*fakeAXNode
	supported     bool
	childrenNodes []*valueSupportAXNode
	supportReads  int
}

type timeoutStageSource struct {
	*fake.CaptureSource
	stage ReadStage
}

func (s *timeoutStageSource) Capability(ctx context.Context) (platform.CapabilityReport, error) {
	if s.stage == ReadStageCapability {
		<-ctx.Done()
		return platform.CapabilityReport{}, ctx.Err()
	}
	return s.CaptureSource.Capability(ctx)
}
func (s *timeoutStageSource) UserIdle(ctx context.Context) (bool, error) {
	if s.stage == ReadStagePresence {
		<-ctx.Done()
		return false, ctx.Err()
	}
	return false, nil
}
func (s *timeoutStageSource) Probe(ctx context.Context) (platform.CaptureProbe, error) {
	if s.stage == ReadStageProbe {
		<-ctx.Done()
		return platform.CaptureProbe{}, ctx.Err()
	}
	return s.CaptureSource.Probe(ctx)
}

func TestCapturePreReadTimeoutReportsActualStageWithoutReadingText(t *testing.T) {
	for _, stage := range []ReadStage{ReadStageCapability, ReadStagePresence, ReadStageProbe} {
		t.Run(string(stage), func(t *testing.T) {
			limits := DefaultLimits()
			limits.MaxDuration = 5 * time.Millisecond
			reads := 0
			p := Pipeline{Source: &timeoutStageSource{CaptureSource: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "synthetic", AppID: "com.kagi.kagimacos"}}, stage: stage}, Limits: limits,
				Read: func(context.Context, platform.CaptureProbe, Limits) (rawFrame, error) {
					reads++
					return rawFrame{}, nil
				},
			}
			_, err := p.Capture(context.Background())
			if !errors.Is(err, context.DeadlineExceeded) || ReadIssueForError(err).Stage != stage || reads != 0 {
				t.Fatal("pre-read timeout lacks its actual closed stage", err, ReadIssueForError(err), reads)
			}
		})
	}
}

func (n *valueSupportAXNode) supportsValue(context.Context, string) (bool, error) {
	n.supportReads++
	return n.supported, nil
}
func (n *valueSupportAXNode) children(_ context.Context, max int) ([]axTreeNode, bool, error) {
	children := make([]axTreeNode, min(max, len(n.childrenNodes)))
	for i := range children {
		children[i] = n.childrenNodes[i]
	}
	return children, len(children) < len(n.childrenNodes), nil
}
func (n *valueSupportAXNode) value(ctx context.Context, max int) (string, error) {
	if !n.supported {
		n.valueReads++
		// A valueless container can spend the remaining message budget on an
		// accessor we already know it does not support.
		<-ctx.Done()
		return "", ctx.Err()
	}
	return n.fakeAXNode.value(ctx, max)
}

func TestAXPositiveValueMetadataSkipsContainersWithoutLosingVerifiedText(t *testing.T) {
	root := &valueSupportAXNode{fakeAXNode: &fakeAXNode{id: 1, roleName: "AXWebArea"}, supported: true}
	for i := 0; i < 32; i++ {
		root.childrenNodes = append(root.childrenNodes, &valueSupportAXNode{fakeAXNode: &fakeAXNode{id: uintptr(i + 2), roleName: "AXGroup"}})
	}
	leaf := &valueSupportAXNode{fakeAXNode: &fakeAXNode{id: 100, roleName: "AXStaticText", text: "A verified paragraph."}, supported: true}
	root.childrenNodes = append(root.childrenNodes, leaf)
	limits := DefaultLimits()
	limits.MaxDuration = 30 * time.Millisecond
	p := Pipeline{Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "synthetic", AppID: "com.kagi.kagimacos"}}, Limits: limits,
		Read: func(ctx context.Context, _ platform.CaptureProbe, limits Limits) (rawFrame, error) {
			frame, err := collectAXFrame(ctx, root, limits)
			frame.domain, frame.domainKnown = "example.test", true
			frame.validate, frame.validateDomain = func(ctx context.Context) error { return ctx.Err() }, func(ctx context.Context, _ string) error { return ctx.Err() }
			return frame, err
		},
	}
	got, err := p.Capture(context.Background())
	if err != nil || got.Text() != leaf.text || got.Truncated() {
		t.Fatal("valueless browser containers consumed the text budget", err)
	}
	for _, node := range root.childrenNodes {
		if node.supportReads != 1 || !node.supported && node.valueReads != 0 || node.releases != 1 {
			t.Fatal("container metadata changed complete structural ownership or invoked an absent value", node.supportReads, node.valueReads, node.releases)
		}
	}
	if root.releases != 1 || leaf.valueReads != 1 {
		t.Fatal("readable value or root ownership changed")
	}
}

func TestReadContextStagePreservesTimeoutAndClosedMetadata(t *testing.T) {
	err := contextualReadError(ReadStageStructure, context.DeadlineExceeded)
	converted := contextError(err)
	var captureErr *CaptureError
	if !errors.As(converted, &captureErr) || captureErr.Code != TimedOut || !errors.Is(converted, context.DeadlineExceeded) || ReadIssueForError(converted).Stage != ReadStageStructure {
		t.Fatal("timeout lost its closed acquisition stage", converted)
	}
}

func TestCaptureTimeoutPreservesNativeAcquisitionAndValueStage(t *testing.T) {
	for _, stage := range []ReadStage{ReadStageBrowserPage, ReadStageBrowserDomain, ReadStageStructure, ReadStageText, ReadStageValidation} {
		t.Run(string(stage), func(t *testing.T) {
			limits := DefaultLimits()
			limits.MaxDuration = 5 * time.Millisecond
			p := Pipeline{Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "synthetic", AppID: "com.kagi.kagimacos"}}, Limits: limits,
				Read: func(ctx context.Context, _ platform.CaptureProbe, _ Limits) (rawFrame, error) {
					if stage != ReadStageText && stage != ReadStageValidation {
						<-ctx.Done()
						return rawFrame{}, contextualReadError(stage, ctx.Err())
					}
					frame := rawFrame{roots: []*rawNode{{role: "AXStaticText", declaredBytes: -1, boundedValue: true, value: func(ctx context.Context, _ int) (string, error) {
						if stage != ReadStageText {
							return "synthetic", nil
						}
						<-ctx.Done()
						return "", contextualReadError(stage, ctx.Err())
					}}}}
					if stage == ReadStageValidation {
						frame.validate = func(ctx context.Context) error { <-ctx.Done(); return contextualReadError(stage, ctx.Err()) }
					}
					return frame, nil
				},
			}
			got, err := p.Capture(context.Background())
			var classified *CaptureError
			if !errors.As(err, &classified) || classified.Code != TimedOut || !errors.Is(err, context.DeadlineExceeded) || ReadIssueForError(err).Stage != stage || got.Text() != "" {
				t.Fatal("expired capture lost acquisition stage or exposed unvalidated text", err, ReadIssueForError(err))
			}
		})
	}
}

func TestCaptureCallerCancellationWinsOverEarlierNativeTimeoutStage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := Pipeline{Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "synthetic", AppID: "com.kagi.kagimacos"}}, Limits: DefaultLimits(),
		Read: func(context.Context, platform.CaptureProbe, Limits) (rawFrame, error) {
			cancel()
			return rawFrame{}, contextualReadError(ReadStageStructure, context.DeadlineExceeded)
		},
	}
	got, err := p.Capture(ctx)
	var classified *CaptureError
	if !errors.As(err, &classified) || classified.Code != Cancelled || !errors.Is(err, context.Canceled) || got.Text() != "" {
		t.Fatal("diagnostic stage overrode explicit caller cancellation", err)
	}
}
