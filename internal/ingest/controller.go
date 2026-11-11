package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"

	"github.com/hazcod/blur/internal/platform"
)

var ErrCaptureNotAvailable = errors.New("ingest: capture capability is not available")

// Controller owns a user-started capture worker. It never grants or edits OS
// permissions; a supporting source may show native guidance once after an
// explicit capture-enabled startup check. Pause waits for worker termination.
type Controller struct {
	mu                 sync.Mutex
	capability         platform.CapabilityReporter
	newRunner          func(context.Context) (func(context.Context) error, error)
	changes            chan struct{}
	cancel             context.CancelFunc
	done               chan struct{}
	suspendRun         func(context.Context) error
	permissionPrompted bool
	expectedStop       bool
	lastExitUnexpected bool
}

func NewController(capability platform.CapabilityReporter, newRunner func(context.Context) (func(context.Context) error, error)) *Controller {
	return &Controller{capability: capability, newRunner: newRunner, changes: make(chan struct{}, 1)}
}

// Changes returns a coalesced, payload-free notification when a capture worker
// exits. Callers should re-read Running rather than infer state from the event.
func (c *Controller) Changes() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.changes
}

func (c *Controller) Running() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done != nil {
		select {
		case <-c.done:
			c.done = nil
			c.cancel = nil
		default:
		}
	}
	return c.done != nil
}

// UnexpectedExit reports whether the most recently stopped worker ended
// without an explicit Pause request. It is intended for bounded recovery
// decisions; callers must still check their persisted capture preference.
func (c *Controller) UnexpectedExit() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastExitUnexpected
}

// ActiveApplicationID returns the identifier from the source's latest probe,
// when that source supports app-state reporting.
func (c *Controller) ActiveApplicationID() (string, bool) {
	if c == nil {
		return "", false
	}
	reporter, ok := c.capability.(platform.ActiveApplicationReporter)
	if !ok {
		return "", false
	}
	return reporter.ActiveApplicationID()
}

func (c *Controller) Capability(ctx context.Context) (platform.CapabilityReport, error) {
	if c == nil || ctx == nil || c.capability == nil {
		return platform.CapabilityReport{}, ErrInvalidService
	}
	report, err := c.capability.Capability(ctx)
	if err != nil {
		return platform.CapabilityReport{}, ErrCaptureNotAvailable
	}
	if err := report.Validate(); err != nil || report.ID != "capture" {
		return platform.CapabilityReport{}, ErrCaptureNotAvailable
	}
	return report, nil
}

// RequestPermission asks a supporting platform to show the native permission
// guidance once per controller lifetime. It never grants permission itself.
func (c *Controller) RequestPermission(ctx context.Context) error {
	if c == nil || ctx == nil {
		return ErrInvalidService
	}
	requester, ok := c.capability.(platform.CapturePermissionRequester)
	if !ok {
		return nil
	}
	c.mu.Lock()
	if c.permissionPrompted {
		c.mu.Unlock()
		return nil
	}
	c.permissionPrompted = true
	c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return requester.RequestPermission(ctx)
}

func (c *Controller) Start(ctx context.Context) error {
	if c == nil || ctx == nil || c.capability == nil || c.newRunner == nil {
		return ErrInvalidService
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done != nil {
		select {
		case <-c.done:
			c.done = nil
			c.cancel = nil
			c.suspendRun = nil
		default:
			return ErrAlreadyRun
		}
	}
	c.suspendRun = nil
	c.expectedStop = false
	c.lastExitUnexpected = false
	report, err := c.capability.Capability(ctx)
	if err != nil {
		return ErrCaptureNotAvailable
	}
	if report.Validate() != nil || report.ID != "capture" || report.State != platform.Available {
		return ErrCaptureNotAvailable
	}
	run, err := c.newRunner(ctx)
	if err != nil {
		_ = c.suspend(context.Background())
		return err
	}
	if run == nil {
		_ = c.suspend(context.Background())
		return ErrInvalidService
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	var suspendOnce sync.Once
	var suspendErr error
	suspend := func(suspendCtx context.Context) error {
		suspendOnce.Do(func() {
			if source, ok := c.capability.(platform.CaptureSuspender); ok {
				suspendErr = source.Suspend(suspendCtx)
			}
		})
		return suspendErr
	}
	c.cancel, c.done, c.suspendRun = cancel, done, suspend
	go func() {
		debug.SetPanicOnFault(true)
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("capture worker panicked", "panic_type", fmt.Sprintf("%T", recovered), "stack", string(debug.Stack()))
			}
			_ = suspend(context.Background())
			c.mu.Lock()
			c.lastExitUnexpected = !c.expectedStop
			c.mu.Unlock()
			close(done)
			select {
			case c.changes <- struct{}{}:
			default:
			}
		}()
		if err := run(runCtx); err != nil && runCtx.Err() == nil {
			slog.Error("capture worker stopped unexpectedly", "error", err)
		}
	}()
	return nil
}

func (c *Controller) Pause(ctx context.Context) error {
	if c == nil || ctx == nil {
		return ErrInvalidService
	}
	c.mu.Lock()
	done, cancel, suspend := c.done, c.cancel, c.suspendRun
	if done == nil {
		c.lastExitUnexpected = false
	} else {
		c.expectedStop = true
	}
	c.mu.Unlock()
	if done == nil {
		if suspend != nil {
			return suspend(ctx)
		}
		return c.suspend(ctx)
	}
	cancel()
	select {
	case <-done:
		c.mu.Lock()
		c.lastExitUnexpected = false
		if c.done == done {
			c.done = nil
			c.cancel = nil
		}
		c.mu.Unlock()
		if suspend != nil {
			return suspend(ctx)
		}
		return c.suspend(ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Controller) suspend(ctx context.Context) error {
	source, ok := c.capability.(platform.CaptureSuspender)
	if !ok {
		return nil
	}
	return source.Suspend(ctx)
}
