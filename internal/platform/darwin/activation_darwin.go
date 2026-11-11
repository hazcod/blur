package darwin

import (
	"context"
	"errors"
	"runtime"
	"sync"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

var errCaptureSourceClosed = errors.New("darwin: capture source closed")

// Next blocks on NSWorkspace activation notifications. It never polls and the
// notification payload is deliberately ignored; consumers must Probe again.
func (a *Adapter) ensureActivationEvents() error {
	if a == nil {
		return errors.New("darwin: capture source unavailable")
	}
	a.eventMu.Lock()
	defer a.eventMu.Unlock()
	if a.closed {
		return errCaptureSourceClosed
	}
	if a.eventStarted {
		return nil
	}
	if a.events == nil {
		a.events = make(chan struct{}, 1)
	}
	if a.eventDone == nil {
		a.eventDone = make(chan struct{})
	}
	subscribe := a.subscribeActivation
	if subscribe == nil {
		subscribe = nativeActivationSubscription
	}
	stop, err := subscribe(a.events)
	if err != nil {
		return errors.New("darwin: activation observer unavailable")
	}
	a.eventStarted = true
	a.stopEvents = stop
	return nil
}

// Suspend unregisters passive listeners while capture is paused. A later call
// to Capability/Next can register them again.
func (a *Adapter) Suspend(_ context.Context) error {
	if a == nil {
		return nil
	}
	a.eventMu.Lock()
	stop := a.stopEvents
	a.stopEvents = nil
	a.eventStarted = false
	a.eventMu.Unlock()
	if stop != nil {
		stop()
	}
	return nil
}

// Close unregisters the activation listener and wakes any blocked Next call.
func (a *Adapter) Close() error {
	if a == nil {
		return nil
	}
	a.eventMu.Lock()
	if a.closed {
		a.eventMu.Unlock()
		return nil
	}
	a.closed = true
	done := a.eventDone
	if done == nil {
		done = make(chan struct{})
		a.eventDone = done
	}
	close(done)
	a.eventMu.Unlock()
	return a.Suspend(context.Background())
}

var activationHub struct {
	sync.Mutex
	channels map[chan<- struct{}]struct{}
}

var activationBlock struct {
	sync.Once
	block objc.Block
}

var activationAppKit struct {
	sync.Once
	handle uintptr
	err    error
}

func nativeActivationSubscription(events chan<- struct{}) (func(), error) {
	if events == nil {
		return nil, errors.New("activation channel missing")
	}
	var stop func()
	// Autorelease pools are thread-local; pin the full Objective-C setup and
	// teardown interval so Go cannot resume this goroutine on another OS thread.
	err := withPinnedOSThread(runtime.LockOSThread, runtime.UnlockOSThread, func() error {
		var err error
		stop, err = nativeActivationSubscriptionPinned(events)
		return err
	})
	if err != nil {
		return nil, err
	}
	return func() {
		_ = withPinnedOSThread(runtime.LockOSThread, runtime.UnlockOSThread, func() error {
			pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(objc.RegisterName("alloc"))
			pool = pool.Send(objc.RegisterName("init"))
			if pool == 0 {
				return errors.New("autorelease pool unavailable")
			}
			defer pool.Send(objc.RegisterName("drain"))
			stop()
			return nil
		})
	}, nil
}

func nativeActivationSubscriptionPinned(events chan<- struct{}) (func(), error) {
	activationAppKit.Do(func() {
		activationAppKit.handle, activationAppKit.err = purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_NOW|purego.RTLD_LOCAL)
	})
	if activationAppKit.err != nil {
		return nil, errors.New("AppKit unavailable")
	}
	activationBlock.Do(func() {
		activationBlock.block = objc.NewBlock(func(_ objc.Block, _ objc.ID) { signalNativeActivation() })
	})
	pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(objc.RegisterName("alloc"))
	pool = pool.Send(objc.RegisterName("init"))
	if pool == 0 {
		return nil, errors.New("autorelease pool unavailable")
	}
	defer pool.Send(objc.RegisterName("drain"))
	workspaceClass := objc.GetClass("NSWorkspace")
	workspace := objc.ID(workspaceClass).Send(objc.RegisterName("sharedWorkspace"))
	center := workspace.Send(objc.RegisterName("notificationCenter"))
	name := objc.ID(objc.GetClass("NSString")).Send(objc.RegisterName("stringWithUTF8String:"), "NSWorkspaceDidActivateApplicationNotification")
	if center == 0 || name == 0 {
		return nil, errors.New("workspace notification center unavailable")
	}
	token := objc.Send[objc.ID](center, objc.RegisterName("addObserverForName:object:queue:usingBlock:"), name, objc.ID(0), objc.ID(0), objc.ID(activationBlock.block))
	if token == 0 {
		return nil, errors.New("activation observer registration failed")
	}
	activationHub.Lock()
	if activationHub.channels == nil {
		activationHub.channels = make(map[chan<- struct{}]struct{})
	}
	activationHub.channels[events] = struct{}{}
	activationHub.Unlock()
	return func() {
		activationHub.Lock()
		delete(activationHub.channels, events)
		activationHub.Unlock()
		objc.ID(center).Send(objc.RegisterName("removeObserver:"), token)
	}, nil
}

// SubscribeApplicationActivation reports foreground-app changes without
// polling. The notification payload is intentionally discarded; callers must
// re-check the relevant capability when an event arrives.
func SubscribeApplicationActivation() (<-chan struct{}, func(), error) {
	events := make(chan struct{}, 1)
	stop, err := nativeActivationSubscription(events)
	if err != nil {
		return nil, nil, err
	}
	return events, stop, nil
}

// SubscribeSelfActivation reports when Blur itself becomes the active app. It
// intentionally avoids observing other apps' activation events.
func SubscribeSelfActivation() (<-chan struct{}, func(), error) {
	if err := loadActivationAppKit(); err != nil {
		return nil, nil, err
	}
	var events <-chan struct{}
	var stop func()
	err := withPinnedOSThread(runtime.LockOSThread, runtime.UnlockOSThread, func() error {
		var err error
		events, stop, err = subscribeSelfActivationPinned()
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return events, func() {
		_ = withPinnedOSThread(runtime.LockOSThread, runtime.UnlockOSThread, func() error {
			pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(objc.RegisterName("alloc"))
			pool = pool.Send(objc.RegisterName("init"))
			if pool == 0 {
				return errors.New("autorelease pool unavailable")
			}
			defer pool.Send(objc.RegisterName("drain"))
			stop()
			return nil
		})
	}, nil
}

func subscribeSelfActivationPinned() (<-chan struct{}, func(), error) {
	events := make(chan struct{}, 1)
	block := objc.NewBlock(func(_ objc.Block, _ objc.ID) {
		select {
		case events <- struct{}{}:
		default:
		}
	})
	pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(objc.RegisterName("alloc"))
	pool = pool.Send(objc.RegisterName("init"))
	if pool == 0 {
		block.Release()
		return nil, nil, errors.New("darwin: autorelease pool unavailable")
	}
	defer pool.Send(objc.RegisterName("drain"))
	center := objc.ID(objc.GetClass("NSNotificationCenter")).Send(objc.RegisterName("defaultCenter"))
	name := objc.ID(objc.GetClass("NSString")).Send(objc.RegisterName("stringWithUTF8String:"), "NSApplicationDidBecomeActiveNotification")
	token := objc.Send[objc.ID](center, objc.RegisterName("addObserverForName:object:queue:usingBlock:"), name, objc.ID(0), objc.ID(0), objc.ID(block))
	if token == 0 {
		block.Release()
		return nil, nil, errors.New("darwin: self-activation observer registration failed")
	}
	var once sync.Once
	return events, func() {
		once.Do(func() {
			objc.ID(center).Send(objc.RegisterName("removeObserver:"), token)
			block.Release()
		})
	}, nil
}

func loadActivationAppKit() error {
	activationAppKit.Do(func() {
		activationAppKit.handle, activationAppKit.err = purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_NOW|purego.RTLD_LOCAL)
	})
	if activationAppKit.err != nil {
		return errors.New("darwin: AppKit unavailable")
	}
	return nil
}

// DisableAutomaticTermination keeps the menu-bar/background capture process
// alive while its windows are hidden. The activity is released when Run exits.
func DisableAutomaticTermination() (func(), error) {
	if err := loadActivationAppKit(); err != nil {
		return nil, err
	}
	pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(objc.RegisterName("alloc"))
	pool = pool.Send(objc.RegisterName("init"))
	if pool == 0 {
		return nil, errors.New("darwin: autorelease pool unavailable")
	}
	defer pool.Send(objc.RegisterName("drain"))
	process := objc.ID(objc.GetClass("NSProcessInfo")).Send(objc.RegisterName("processInfo"))
	reason := objc.ID(objc.GetClass("NSString")).Send(objc.RegisterName("stringWithUTF8String:"), "Blur continues local capture while its windows are hidden")
	const disableTerminationAndSuddenTermination = uint64(1<<15 | 1<<14)
	activity := objc.Send[objc.ID](process, objc.RegisterName("beginActivityWithOptions:reason:"), disableTerminationAndSuddenTermination, reason)
	if activity == 0 {
		return nil, errors.New("darwin: background activity could not be acquired")
	}
	// The token is autoreleased; keep it alive past pool.drain until endActivity:.
	activity.Send(objc.RegisterName("retain"))
	var once sync.Once
	return func() {
		once.Do(func() {
			objc.ID(process).Send(objc.RegisterName("endActivity:"), activity)
			activity.Send(objc.RegisterName("release"))
		})
	}, nil
}

func signalNativeActivation() {
	activationHub.Lock()
	defer activationHub.Unlock()
	for events := range activationHub.channels {
		select {
		case events <- struct{}{}:
		default: // coalesce bursts; focus is re-probed after wake
		}
	}
}
