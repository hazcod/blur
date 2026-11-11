// Windows UIA policy tests use fakes so the security boundary runs on every CI host.
package capture

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/platform"
	platformwindows "github.com/hazcod/blur/internal/platform/windows"
)

type fakeWindowsAXElement struct {
	id             uintptr
	processIDValue uint32
	controlTypeID  int32
	isPassword     bool
	valueText      string
	childElements  []*fakeWindowsAXElement
	valueReads     int
	childReads     int
	childLimitSeen int
	releases       int
	closeCalls     int
	textDeadline   time.Time
}

func (e *fakeWindowsAXElement) identity() uintptr { return e.id }
func (e *fakeWindowsAXElement) processID(context.Context) (uint32, error) {
	return e.processIDValue, nil
}
func (e *fakeWindowsAXElement) controlType(context.Context) (int32, error) {
	return e.controlTypeID, nil
}
func (e *fakeWindowsAXElement) password(context.Context) (bool, error) { return e.isPassword, nil }
func (e *fakeWindowsAXElement) children(_ context.Context, max int) ([]windowsAXElement, bool, error) {
	e.childReads++
	if max > e.childLimitSeen {
		e.childLimitSeen = max
	}
	count := min(len(e.childElements), max)
	children := make([]windowsAXElement, count)
	for i := range count {
		children[i] = e.childElements[i]
	}
	return children, len(e.childElements) > count, nil
}
func (e *fakeWindowsAXElement) text(ctx context.Context, max int) (string, error) {
	e.valueReads++
	e.textDeadline, _ = ctx.Deadline()
	if len(e.valueText) > max {
		bounded, _ := truncateUTF8(e.valueText, max)
		return bounded, errAXValueTruncated
	}
	return e.valueText, nil
}
func (e *fakeWindowsAXElement) release()      { e.releases++ }
func (e *fakeWindowsAXElement) closeSession() { e.closeCalls++ }

type fakeWindowsVerifiedSource struct {
	current      platformwindows.FocusedTarget
	probe        platform.CaptureProbe
	changeOn     string
	changeNow    bool
	readDeadline time.Time
}

func (s *fakeWindowsVerifiedSource) Capability(context.Context) (platform.CapabilityReport, error) {
	return platform.CapabilityReport{ID: "capture", State: platform.Available, Reason: "available", Evidence: "ui_automation"}, nil
}
func (s *fakeWindowsVerifiedSource) Probe(context.Context) (platform.CaptureProbe, error) {
	return s.probe, nil
}
func (*fakeWindowsVerifiedSource) Next(context.Context) (platform.CaptureEvent, error) {
	return platform.CaptureEvent{}, errors.New("unexpected event wait")
}
func (s *fakeWindowsVerifiedSource) WithVerifiedTarget(ctx context.Context, id string, read func(context.Context, platformwindows.FocusedTarget) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if id != s.probe.TargetID {
		return platform.ErrInvalidLease
	}
	target := s.current
	s.readDeadline, _ = ctx.Deadline()
	if err := read(ctx, target); err != nil {
		return err
	}
	if s.changeNow && s.changeOn == "read" {
		s.current.Window++
		s.changeNow = false
	}
	if s.current != target {
		return platform.ErrInvalidLease
	}
	return ctx.Err()
}

func TestWindowsPipelineReservesProviderCleanupTime(t *testing.T) {
	target := platformwindows.FocusedTarget{Window: 42, ProcessID: 7, AppID: "notepad.exe", CreationTime: 1234}
	source := &fakeWindowsVerifiedSource{current: target, probe: platform.CaptureProbe{TargetID: "windows:test-token", AppID: target.AppID}}
	root := &fakeWindowsAXElement{id: 1, processIDValue: 7, controlTypeID: 50020, valueText: "bounded"}
	limits := Limits{MaxBytes: 128, MaxNodes: 8, MaxDepth: 4, MaxDuration: 600 * time.Millisecond}
	frame, err := readWindowsAX(context.Background(), source, source.probe, limits, func(context.Context, platformwindows.FocusedTarget) (windowsAXElement, error) { return root, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer frame.cleanup()
	if source.readDeadline.IsZero() || time.Until(source.readDeadline) > 450*time.Millisecond {
		t.Fatalf("UIA structure work did not reserve cleanup time: deadline in %v", time.Until(source.readDeadline))
	}
	if _, err := frame.roots[0].value(context.Background(), 128); err != nil {
		t.Fatal(err)
	}
	if root.textDeadline.IsZero() || time.Until(root.textDeadline) > 450*time.Millisecond {
		t.Fatalf("deferred UIA value read did not reserve cleanup time: deadline in %v", time.Until(root.textDeadline))
	}
}

func TestWindowsPipelineRejectsPasswordBeforeReadingValue(t *testing.T) {
	secret := &fakeWindowsAXElement{id: 3, controlTypeID: 50020, valueText: "should never be read"}
	password := &fakeWindowsAXElement{id: 2, controlTypeID: 50004, isPassword: true, childElements: []*fakeWindowsAXElement{secret}}
	root := &fakeWindowsAXElement{id: 1, controlTypeID: 50032, childElements: []*fakeWindowsAXElement{password}}
	frame, err := collectAXFrame(context.Background(), newWindowsAXNode(root), Limits{MaxBytes: 128, MaxNodes: 16, MaxDepth: 8, MaxDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer frame.cleanup()
	if !frame.roots[0].children[0].sensitive || password.childReads != 0 || password.valueReads != 0 || secret.valueReads != 0 {
		t.Fatalf("password subtree was accessed: frame=%+v password=%+v secret=%+v", frame.roots[0].children[0], password, secret)
	}
}

func TestWindowsTreeTraversalStopsAtNodeAndDepthLimits(t *testing.T) {
	root := &fakeWindowsAXElement{id: 1, processIDValue: 7, controlTypeID: 50032}
	for id := uintptr(2); id <= 6; id++ {
		root.childElements = append(root.childElements, &fakeWindowsAXElement{id: id, controlTypeID: 50020, valueText: "line"})
	}
	root.childElements[0].childElements = []*fakeWindowsAXElement{{id: 20, controlTypeID: 50020, valueText: "nested"}}
	frame, err := collectAXFrame(context.Background(), newWindowsAXNode(root), Limits{MaxBytes: 128, MaxNodes: 3, MaxDepth: 2, MaxDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer frame.cleanup()
	if !frame.truncated || len(frame.roots[0].children) != 2 || root.childLimitSeen > 3 {
		t.Fatalf("tree exceeded bounds: truncated=%t children=%d requested=%d", frame.truncated, len(frame.roots[0].children), root.childLimitSeen)
	}
}

func TestWindowsPipelineDiscardsFrameAfterTargetLeaseChanges(t *testing.T) {
	target := platformwindows.FocusedTarget{Window: 42, ProcessID: 7, AppID: "notepad.exe", CreationTime: 1234}
	source := &fakeWindowsVerifiedSource{current: target, probe: platform.CaptureProbe{TargetID: "windows:test-token", AppID: target.AppID}}
	root := &fakeWindowsAXElement{id: 1, processIDValue: 7, controlTypeID: 50032, valueText: "private text"}
	source.changeOn, source.changeNow = "read", true
	_, err := readWindowsAX(context.Background(), source, source.probe, Limits{MaxBytes: 128, MaxNodes: 8, MaxDepth: 4, MaxDuration: time.Second}, func(context.Context, platformwindows.FocusedTarget) (windowsAXElement, error) { return root, nil })
	if !errors.Is(err, platform.ErrInvalidLease) {
		t.Fatalf("readWindowsAX() error = %v, want stale lease rejection", err)
	}
	if root.valueReads != 0 {
		t.Fatalf("stale frame read %d text values", root.valueReads)
	}
	if root.closeCalls != 1 {
		t.Fatalf("stale frame closed UIA session %d times, want once", root.closeCalls)
	}
}

func TestWindowsPipelineRejectsUIARootFromDifferentProcess(t *testing.T) {
	target := platformwindows.FocusedTarget{Window: 42, ProcessID: 7, AppID: "notepad.exe", CreationTime: 1234}
	source := &fakeWindowsVerifiedSource{current: target, probe: platform.CaptureProbe{TargetID: "windows:test-token", AppID: target.AppID}}
	root := &fakeWindowsAXElement{id: 1, processIDValue: 8, controlTypeID: 50032, valueText: "must not be read"}
	_, err := readWindowsAX(context.Background(), source, source.probe, Limits{MaxBytes: 128, MaxNodes: 8, MaxDepth: 4, MaxDuration: time.Second}, func(context.Context, platformwindows.FocusedTarget) (windowsAXElement, error) { return root, nil })
	if !errors.Is(err, platform.ErrInvalidLease) {
		t.Fatalf("readWindowsAX() error = %v, want mismatched UIA process rejection", err)
	}
	if root.childReads != 0 || root.valueReads != 0 || root.releases != 1 || root.closeCalls != 1 {
		t.Fatalf("mismatched root accessed or leaked: %+v", root)
	}
}

func TestWindowsPipelineRedactsBeforeObservationConstruction(t *testing.T) {
	target := platformwindows.FocusedTarget{Window: 42, ProcessID: 7, AppID: "notepad.exe", CreationTime: 1234}
	source := &fakeWindowsVerifiedSource{current: target, probe: platform.CaptureProbe{TargetID: "windows:test-token", AppID: target.AppID}}
	root := &fakeWindowsAXElement{id: 1, processIDValue: 7, controlTypeID: 50032, valueText: "Authorization: Bearer abcdefghijklmnop"}
	pipeline := Pipeline{Source: source, Limits: Limits{MaxBytes: 256, MaxNodes: 8, MaxDepth: 4, MaxDuration: time.Second}}
	pipeline.Read = func(ctx context.Context, probe platform.CaptureProbe, limits Limits) (rawFrame, error) {
		return readWindowsAX(ctx, source, probe, limits, func(context.Context, platformwindows.FocusedTarget) (windowsAXElement, error) { return root, nil })
	}
	observation, err := pipeline.Capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(observation.Text(), "abcdefghijklmnop") || !strings.Contains(observation.Text(), "[REDACTED") {
		t.Fatalf("observation was not redacted before leaving the pipeline: %q", observation.Text())
	}
}

func TestWindowsPipelinePreservesBoundedValueTruncation(t *testing.T) {
	target := platformwindows.FocusedTarget{Window: 42, ProcessID: 7, AppID: "notepad.exe", CreationTime: 1234}
	source := &fakeWindowsVerifiedSource{current: target, probe: platform.CaptureProbe{TargetID: "windows:test-token", AppID: target.AppID}}
	root := &fakeWindowsAXElement{id: 1, processIDValue: 7, controlTypeID: 50020, valueText: "123456789"}
	pipeline := Pipeline{Source: source, Limits: Limits{MaxBytes: 8, MaxNodes: 8, MaxDepth: 4, MaxDuration: time.Second}}
	pipeline.Read = func(ctx context.Context, probe platform.CaptureProbe, limits Limits) (rawFrame, error) {
		return readWindowsAX(ctx, source, probe, limits, func(context.Context, platformwindows.FocusedTarget) (windowsAXElement, error) { return root, nil })
	}
	observation, err := pipeline.Capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Truncated() || len(observation.Text()) > 8 {
		t.Fatalf("bounded observation = %q, truncated=%t", observation.Text(), observation.Truncated())
	}
}

func TestWindowsUICallReturnsOnDeadlineAndReleasesLateResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := make(chan struct{})
	finish := make(chan struct{})
	discarded := make(chan struct{})
	value, err := windowsUIACall(ctx, func() (*fakeWindowsAXElement, error) {
		close(started)
		<-finish
		return &fakeWindowsAXElement{id: 99}, nil
	}, func(element *fakeWindowsAXElement) {
		element.release()
		close(discarded)
	})
	<-started
	if value != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline result = (%v, %v)", value, err)
	}
	close(finish)
	select {
	case <-discarded:
	case <-time.After(time.Second):
		t.Fatal("late UIA result was not released")
	}
}

func TestWindowsControlTypeMappingUsesStableAXRoles(t *testing.T) {
	for _, test := range []struct {
		controlType int32
		want        string
	}{{50002, "AXCheckBox"}, {50004, "AXTextField"}, {50009, "AXMenu"}, {50010, "AXMenuBar"}, {50011, "AXMenuItem"}, {50012, "AXProgressIndicator"}, {50014, "AXScrollBar"}, {50017, "AXStatusBar"}, {50018, "AXTabGroup"}, {50019, "AXTab"}, {50020, "AXStaticText"}, {50022, "AXTooltip"}, {50030, "AXDocument"}, {50032, "AXWindow"}, {50038, "AXSeparator"}, {50040, "AXToolbar"}, {59999, ""}} {
		if got := windowsControlRole(test.controlType); got != test.want {
			t.Errorf("windowsControlRole(%d) = %q, want %q", test.controlType, got, test.want)
		}
	}
}

func TestWindowsTextUnitLimitUsesConfiguredBudget(t *testing.T) {
	for _, test := range []struct{ max, want int }{{0, 1}, {1, 1}, {8, 8}, {256 * 1024, 256 * 1024}} {
		if got := windowsTextUnitLimit(test.max); got != test.want {
			t.Errorf("windowsTextUnitLimit(%d) = %d, want %d", test.max, got, test.want)
		}
	}
}

func TestWindowsChildCapacityAvoidsWorstCasePreallocation(t *testing.T) {
	for _, test := range []struct{ max, want int }{{1, 1}, {16, 16}, {17, 16}, {2048, 16}} {
		if got := windowsChildInitialCapacity(test.max); got != test.want {
			t.Errorf("windowsChildInitialCapacity(%d) = %d, want %d", test.max, got, test.want)
		}
	}
}
