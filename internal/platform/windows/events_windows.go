//go:build windows

package windows

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"unsafe"

	"github.com/hazcod/blur/internal/platform"
	"golang.org/x/sys/windows"
)

const (
	winEventOutOfContext = 0x0000
	winEventSkipOwnProc  = 0x0002
	winEventFlags        = winEventOutOfContext | winEventSkipOwnProc
	wmQuit               = 0x0012
	pmNoRemove           = 0x0000
)

var (
	user32Events          = windows.NewLazySystemDLL("user32.dll")
	procSetWinEventHook   = user32Events.NewProc("SetWinEventHook")
	procUnhookWinEvent    = user32Events.NewProc("UnhookWinEvent")
	procPeekMessage       = user32Events.NewProc("PeekMessageW")
	procGetMessage        = user32Events.NewProc("GetMessageW")
	procPostThreadMessage = user32Events.NewProc("PostThreadMessageW")
)

type nativePoint struct{ X, Y int32 }
type nativeMessage struct {
	Window  uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Point   nativePoint
	Private uint32
}

type nativeEventHook struct {
	mu            sync.Mutex
	thread        uint32
	stopRequested bool
	done          chan struct{}
	events        chan platform.CaptureEvent
	once          sync.Once
}

var (
	nativeEventHooks sync.Map // map[HWINEVENTHOOK]*nativeEventHook
	nativeEventProc  = windows.NewCallback(nativeWinEventCallback)
)

func newNativeEventSubscription(ctx context.Context) (eventSubscription, error) {
	if ctx == nil || ctx.Err() != nil {
		if ctx == nil {
			return eventSubscription{}, platform.ErrInvalidLease
		}
		return eventSubscription{}, ctx.Err()
	}
	hook := &nativeEventHook{
		done:   make(chan struct{}),
		events: make(chan platform.CaptureEvent, 1),
	}
	ready := make(chan error, 1)
	go hook.run(ready)
	select {
	case err := <-ready:
		if err != nil {
			<-hook.done
			return eventSubscription{}, err
		}
	case <-ctx.Done():
		hook.stopHook()
		<-hook.done
		return eventSubscription{}, ctx.Err()
	}
	return eventSubscription{
		events: hook.events,
		stop:   hook.stopHook,
		done:   hook.done,
	}, nil
}

func (h *nativeEventHook) run(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(h.done)

	// Force creation of this thread's queue before Subscribe returns; otherwise
	// PostThreadMessage(WM_QUIT) can race queue creation during Suspend.
	var msg nativeMessage
	procPeekMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0, pmNoRemove)
	h.mu.Lock()
	h.thread = windows.GetCurrentThreadId()
	stopRequested := h.stopRequested
	h.mu.Unlock()
	if stopRequested {
		return
	}
	hooks := make([]uintptr, 0, 2)
	for _, event := range []uint32{eventSystemForeground, eventObjectFocus} {
		handle, _, callErr := procSetWinEventHook.Call(
			uintptr(event), uintptr(event), 0, nativeEventProc, 0, 0, winEventFlags,
		)
		if handle == 0 {
			for _, registered := range hooks {
				nativeEventHooks.Delete(registered)
				procUnhookWinEvent.Call(registered)
			}
			ready <- errors.New("windows: event subscription unavailable")
			_ = callErr
			return
		}
		nativeEventHooks.Store(handle, h)
		hooks = append(hooks, handle)
	}
	ready <- nil
	defer func() {
		for _, registered := range hooks {
			nativeEventHooks.Delete(registered)
			procUnhookWinEvent.Call(registered)
		}
	}()
	h.mu.Lock()
	stopRequested = h.stopRequested
	h.mu.Unlock()
	if stopRequested {
		return
	}

	for {
		result, _, callErr := procGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		switch int32(result) {
		case 0:
			return
		case -1:
			_ = callErr
			return
		default:
			// WinEvent out-of-context callbacks run on this registering thread.
			// We own no application messages, so draining them is sufficient.
		}
	}
}

func (h *nativeEventHook) stopHook() {
	h.once.Do(func() {
		h.mu.Lock()
		h.stopRequested = true
		thread := h.thread
		h.mu.Unlock()
		if thread != 0 {
			procPostThreadMessage.Call(uintptr(thread), wmQuit, 0, 0)
		}
	})
}

func nativeWinEventCallback(hook uintptr, event uint32, hwnd uintptr, _ int32, _ int32, _ uint32, _ uint32) uintptr {
	value, ok := nativeEventHooks.Load(hook)
	if !ok {
		return 0
	}
	h := value.(*nativeEventHook)
	kind := winEventKind(event, hwnd)
	if kind == "" {
		return 0
	}
	next := platform.CaptureEvent{Kind: kind}
	select {
	case h.events <- next:
	default:
		// A full one-slot queue means an earlier event is stale; retain only
		// the newest metadata notification and never block the OS callback.
		select {
		case <-h.events:
		default:
		}
		select {
		case h.events <- next:
		default:
		}
	}
	return 0
}
