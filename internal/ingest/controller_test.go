package ingest

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/platform"
)

type testControllerSource struct {
	state   platform.CapabilityState
	started atomic.Int32
	stopped atomic.Int32
	suspend atomic.Int32
}

func (s *testControllerSource) Suspend(context.Context) error {
	s.suspend.Add(1)
	return nil
}

func (s *testControllerSource) Capability(context.Context) (platform.CapabilityReport, error) {
	return platform.CapabilityReport{ID: "capture", State: s.state, Reason: "available", Evidence: "test"}, nil
}

// TestControllerRequiresCapabilityAndWaitsForPausedWorker describes the
// explicit-start contract without constructing any platform capture adapter.
func TestControllerRequiresCapabilityAndWaitsForPausedWorker(t *testing.T) {
	// The concrete service lifecycle is exercised through a minimal controller
	// runner to prove pause waits for cancellation before reporting stopped.
	source := &testControllerSource{state: platform.PermissionRequired}
	c := NewController(source, func(context.Context) (func(context.Context) error, error) {
		return func(ctx context.Context) error {
			source.started.Add(1)
			<-ctx.Done()
			source.stopped.Add(1)
			return ctx.Err()
		}, nil
	})
	if err := c.Start(context.Background()); err == nil {
		t.Fatal("Start() accepted missing permission")
	}
	if c.Running() {
		t.Fatal("controller running without permission")
	}
	source.state = platform.Available
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for source.started.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if source.started.Load() == 0 {
		t.Fatal("worker did not start")
	}
	if err := c.Pause(context.Background()); err != nil {
		t.Fatalf("Pause(): %v", err)
	}
	if source.stopped.Load() != 1 || c.Running() || source.suspend.Load() != 1 {
		t.Fatalf("Pause returned before stopped/suspend: stopped=%d suspend=%d running=%v", source.stopped.Load(), source.suspend.Load(), c.Running())
	}
}

func TestControllerReportsUnexpectedWorkerExit(t *testing.T) {
	source := &testControllerSource{state: platform.Available}
	c := NewController(source, func(context.Context) (func(context.Context) error, error) {
		return func(context.Context) error { return errors.New("fixture worker failure") }, nil
	})
	changes := c.Changes()
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changes:
	case <-time.After(time.Second):
		t.Fatal("capture worker exit did not emit a state-change notification")
	}
	deadline := time.Now().Add(time.Second)
	for c.Running() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if c.Running() || source.suspend.Load() != 1 {
		t.Fatalf("after worker exit: running=%v suspend=%d", c.Running(), source.suspend.Load())
	}
}

func TestControllerSuspendsSourceWhenRunnerConstructionFails(t *testing.T) {
	source := &testControllerSource{state: platform.Available}
	c := NewController(source, func(context.Context) (func(context.Context) error, error) {
		return nil, ErrInvalidService
	})
	if err := c.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded without a runner")
	}
	if source.suspend.Load() != 1 {
		t.Fatalf("suspend count = %d, want 1", source.suspend.Load())
	}
}
