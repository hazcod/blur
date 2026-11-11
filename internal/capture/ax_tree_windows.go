//go:build windows

package capture

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"unicode/utf16"

	platformwindows "github.com/hazcod/blur/internal/platform/windows"
	"github.com/lpintes/walk/uia"
)

type windowsUIASession struct {
	automation *uia.Automation
	walker     *uia.TreeWalker
	nextID     atomic.Uint64
	pending    sync.WaitGroup
	callMu     sync.Mutex
	closing    bool
	release    func()
	closeOnce  sync.Once
}

var windowsUIASessionSlots = newWindowsUIASessionGate()

type nativeWindowsAXElement struct {
	session *windowsUIASession
	element *uia.Element
	id      uintptr
	root    bool
}

func newWindowsUIARoot(ctx context.Context, target platformwindows.FocusedTarget) (windowsAXElement, error) {
	release, err := windowsUIASessionSlots.acquire(ctx)
	if err != nil {
		return nil, errors.New("capture: UI Automation session unavailable")
	}
	automation, transferred, err := windowsUIACallWithAbandon(ctx, uia.New, func(value *uia.Automation) {
		if value != nil {
			_ = value.Close()
		}
	}, func(_ *uia.Automation, _ error) { release() })
	if err != nil {
		if transferred {
			release()
		}
		return nil, errors.New("capture: UI Automation unavailable")
	}
	if automation == nil {
		release()
		return nil, errors.New("capture: UI Automation unavailable")
	}
	if err := ctx.Err(); err != nil {
		windowsUIACleanupAsync(nil, func() { _ = automation.Close() }, release)
		return nil, err
	}
	session := &windowsUIASession{automation: automation, walker: automation.ControlView(), release: release}
	root := &nativeWindowsAXElement{session: session, id: uintptr(session.nextID.Add(1)), root: true}
	element, err := windowsUIASessionCall(ctx, session, func() (*uia.Element, error) {
		return automation.ElementFromHandle(target.Window)
	}, releaseUIAElement)
	if err != nil || element == nil {
		session.close()
		return nil, errors.New("capture: foreground UI Automation root unavailable")
	}
	root.element = element
	return root, nil
}

func (e *nativeWindowsAXElement) identity() uintptr { return e.id }

func (e *nativeWindowsAXElement) processID(ctx context.Context) (uint32, error) {
	return windowsUIASessionCall(ctx, e.session, func() (uint32, error) {
		processID, err := e.element.ProcessID()
		if err != nil || processID <= 0 || uint64(processID) > uint64(^uint32(0)) {
			return 0, errors.New("capture: UI Automation process identity unavailable")
		}
		return uint32(processID), nil
	}, nil)
}

func (e *nativeWindowsAXElement) controlType(ctx context.Context) (int32, error) {
	return windowsUIASessionCall(ctx, e.session, func() (int32, error) {
		controlType, err := e.element.ControlType()
		return int32(controlType), err
	}, nil)
}

func (e *nativeWindowsAXElement) password(ctx context.Context) (bool, error) {
	return windowsUIASessionCall(ctx, e.session, e.element.IsPassword, nil)
}

func (e *nativeWindowsAXElement) children(ctx context.Context, max int) ([]windowsAXElement, bool, error) {
	if max < 1 {
		return nil, false, errors.New("capture: invalid Windows child limit")
	}
	children := make([]windowsAXElement, 0, windowsChildInitialCapacity(max))
	child, err := windowsUIASessionCall(ctx, e.session, func() (*uia.Element, error) {
		return e.session.walker.FirstChild(e.element)
	}, releaseUIAElement)
	if err != nil {
		return nil, false, err
	}
	for child != nil && len(children) < max {
		children = append(children, &nativeWindowsAXElement{
			session: e.session, element: child, id: uintptr(e.session.nextID.Add(1)),
		})
		child, err = windowsUIASessionCall(ctx, e.session, func() (*uia.Element, error) {
			return e.session.walker.NextSibling(children[len(children)-1].(*nativeWindowsAXElement).element)
		}, releaseUIAElement)
		if err != nil {
			releaseUIAElement(child)
			releaseWindowsElements(children)
			return nil, false, err
		}
	}
	more := child != nil
	if child != nil {
		_ = child.Release()
	}
	return children, more, nil
}

func (e *nativeWindowsAXElement) text(ctx context.Context, max int) (string, error) {
	if max < 0 {
		return "", errors.New("capture: invalid Windows text limit")
	}
	// UIA TextRange.GetText caps UTF-16 code units, not UTF-8 bytes. Read at most
	// the configured byte budget in code units, then the generic adapter clips
	// converted text to that exact byte limit. This bounds transient output to
	// at most 3x the configured bytes without throwing away ASCII capacity.
	// ValuePattern is intentionally not used: it returns unbounded text.
	unitLimit := windowsTextUnitLimit(max)
	text, err := windowsUIASessionCall(ctx, e.session, func() (string, error) {
		return e.element.TextBounded(unitLimit)
	}, nil)
	if err != nil || text == "" {
		return text, err
	}
	if len(utf16.Encode([]rune(text))) >= unitLimit {
		return text, errAXValueTruncated
	}
	return text, nil
}

func (e *nativeWindowsAXElement) release() {
	if e != nil && e.element != nil {
		// UIA providers may stall the Automation worker. Queue COM release on
		// that worker rather than making Pause/shutdown wait for it.
		e.element.ReleaseLater()
		e.element = nil
	}
}

func (e *nativeWindowsAXElement) closeSession() {
	if e == nil || !e.root || e.session == nil {
		return
	}
	e.session.close()
}

func windowsUIASessionCall[T any](ctx context.Context, session *windowsUIASession, call func() (T, error), discard func(T)) (T, error) {
	if session == nil {
		var zero T
		return zero, errors.New("capture: UI Automation session unavailable")
	}
	if ctx == nil {
		var zero T
		return zero, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		var zero T
		return zero, err
	}
	if !session.beginCall() {
		var zero T
		return zero, errors.New("capture: UI Automation session is closing")
	}
	result := make(chan struct {
		value T
		err   error
	})
	go func() {
		defer session.pending.Done()
		if ctx.Err() != nil {
			return
		}
		value, err := call()
		completed := struct {
			value T
			err   error
		}{value: value, err: err}
		select {
		case result <- completed:
		case <-ctx.Done():
			if discard != nil {
				discard(value)
			}
		}
	}()
	select {
	case completed := <-result:
		return completed.value, completed.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

func (s *windowsUIASession) close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.callMu.Lock()
		s.closing = true
		s.callMu.Unlock()
		windowsUIACleanupAsync(&s.pending, func() { _ = s.automation.Close() }, s.release)
	})
}

func (s *windowsUIASession) beginCall() bool {
	s.callMu.Lock()
	defer s.callMu.Unlock()
	if s.closing {
		return false
	}
	s.pending.Add(1)
	return true
}

func releaseUIAElement(element *uia.Element) {
	if element != nil {
		element.ReleaseLater()
	}
}
