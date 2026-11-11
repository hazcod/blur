// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package uia

import (
	"errors"
	"fmt"
	"image"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"github.com/lpintes/walk/internal/win"
)

var (
	// ErrClosed is returned by the methods of an Automation after Close,
	// and by the methods of its elements and tree walkers.
	ErrClosed = errors.New("uia: automation is closed")

	// ErrReleased is returned by the methods of an Element after Release.
	ErrReleased = errors.New("uia: element is released")

	// ErrElementNotAvailable is wrapped by the errors returned for an
	// element that no longer exists, for example because its window was
	// closed.
	ErrElementNotAvailable = errors.New("uia: element not available")

	errOtherAutomation = errors.New("uia: element of another Automation")
	errNilElement      = errors.New("uia: nil element")
)

// hresultError returns the error for a failed UI Automation call.
func hresultError(op string, hr win.HRESULT) error {
	if uint32(hr) == win.UIA_E_ELEMENTNOTAVAILABLE {
		return fmt.Errorf("uia: %s: %w", op, ErrElementNotAvailable)
	}
	return fmt.Errorf("uia: %s: %w", op, syscall.Errno(uint32(hr)))
}

// Automation is a connection to UI Automation. Create it with New and
// close it with Close.
type Automation struct {
	mu     sync.RWMutex // guards closed and sending to calls
	closed bool
	calls  chan func()
	done   chan struct{}

	releaseMu sync.Mutex
	releases  []func() // releases queued by finalizers

	// Used only on the thread of the Automation.
	uia           *win.IUIAutomation
	uia5          *win.IUIAutomation5             // see automation5
	walkers       [3]*win.IUIAutomationTreeWalker // see TreeWalker.view
	subscriptions map[*Subscription]struct{}
}

// New connects to UI Automation. It needs Windows 8 or later.
func New() (*Automation, error) {
	a := &Automation{
		calls: make(chan func()),
		done:  make(chan struct{}),
	}
	started := make(chan error)
	go a.run(started)
	if err := <-started; err != nil {
		return nil, err
	}
	return a, nil
}

// run is the goroutine of the thread that makes all UI Automation calls.
func (a *Automation) run(started chan<- error) {
	// The thread stays locked: it is in a COM apartment and ends with the
	// goroutine.
	runtime.LockOSThread()
	defer close(a.done)

	if hr := win.CoInitializeEx(nil, win.COINIT_MULTITHREADED); win.FAILED(hr) {
		started <- hresultError("CoInitializeEx", hr)
		return
	}
	defer win.CoUninitialize()

	var p unsafe.Pointer
	if hr := win.CoCreateInstance(&win.CLSID_CUIAutomation8, nil, win.CLSCTX_INPROC_SERVER, &win.IID_IUIAutomation, &p); win.FAILED(hr) {
		started <- hresultError("CoCreateInstance CUIAutomation8", hr)
		return
	}
	a.uia = (*win.IUIAutomation)(p)
	started <- nil

	for f := range a.calls {
		a.runReleases()
		f()
	}

	for s := range a.subscriptions {
		s.stop()
		a.unsubscribe(s)
	}
	a.runReleases()
	for _, w := range a.walkers {
		if w != nil {
			w.Release()
		}
	}
	if a.uia5 != nil {
		a.uia5.Release()
	}
	a.uia.Release()
}

// do runs f on the thread of the Automation and returns its error.
func (a *Automation) do(f func() error) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		return ErrClosed
	}
	errc := make(chan error, 1)
	a.calls <- func() { errc <- f() }
	return <-errc
}

// releaseLater queues release to run on the thread of the Automation
// before its next call. Finalizers use it; they must not block.
func (a *Automation) releaseLater(release func()) {
	a.releaseMu.Lock()
	a.releases = append(a.releases, release)
	a.releaseMu.Unlock()
}

func (a *Automation) runReleases() {
	a.releaseMu.Lock()
	releases := a.releases
	a.releases = nil
	a.releaseMu.Unlock()
	for _, release := range releases {
		release()
	}
}

// Close disconnects from UI Automation. It waits for running calls to
// finish and closes the subscriptions to events. The elements of the
// Automation stop working; their methods return ErrClosed. Close returns
// nil, also when called again.
func (a *Automation) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	close(a.calls)
	a.mu.Unlock()
	<-a.done
	return nil
}

// newElement wraps p, which may be nil. It runs on the thread of the
// Automation.
func (a *Automation) newElement(p *win.IUIAutomationElement) *Element {
	if p == nil {
		return nil
	}
	e := &Element{a: a, p: p}
	runtime.SetFinalizer(e, func(e *Element) {
		e.a.releaseLater(func() { p.Release() })
	})
	return e
}

// element runs get on the thread of the Automation and wraps the element
// it returns.
func (a *Automation) element(op string, get func(p **win.IUIAutomationElement) win.HRESULT) (*Element, error) {
	var e *Element
	err := a.do(func() error {
		var p *win.IUIAutomationElement
		if hr := get(&p); win.FAILED(hr) {
			return hresultError(op, hr)
		}
		e = a.newElement(p)
		return nil
	})
	return e, err
}

// Root returns the root element, the desktop.
func (a *Automation) Root() (*Element, error) {
	return a.element("GetRootElement", func(p **win.IUIAutomationElement) win.HRESULT {
		return a.uia.GetRootElement(p)
	})
}

// ElementFromHandle returns the element of a window, given its handle. For
// a walk window w, the handle is uintptr(w.Handle()).
func (a *Automation) ElementFromHandle(hwnd uintptr) (*Element, error) {
	return a.element("ElementFromHandle", func(p **win.IUIAutomationElement) win.HRESULT {
		return a.uia.ElementFromHandle(win.HWND(hwnd), p)
	})
}

// ElementFromPoint returns the element at a point of the screen, in
// physical pixels.
func (a *Automation) ElementFromPoint(pt image.Point) (*Element, error) {
	return a.element("ElementFromPoint", func(p **win.IUIAutomationElement) win.HRESULT {
		return a.uia.ElementFromPoint(win.POINT{X: int32(pt.X), Y: int32(pt.Y)}, p)
	})
}

// FocusedElement returns the element that has the keyboard focus.
func (a *Automation) FocusedElement() (*Element, error) {
	return a.element("GetFocusedElement", func(p **win.IUIAutomationElement) win.HRESULT {
		return a.uia.GetFocusedElement(p)
	})
}
