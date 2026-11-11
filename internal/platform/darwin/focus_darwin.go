package darwin

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

type axFocusAPI struct {
	appServices         uintptr
	coreFoundation      uintptr
	observerCreate      func(int32, uintptr, *uintptr) int32
	observerAdd         func(uintptr, uintptr, uintptr, uintptr) int32
	observerSource      func(uintptr) uintptr
	createApplication   func(int32) uintptr
	runLoopCurrent      func() uintptr
	runLoopAddSource    func(uintptr, uintptr, uintptr)
	runLoopRemoveSource func(uintptr, uintptr, uintptr)
	runLoopRun          func()
	runLoopStop         func(uintptr)
	runLoopWake         func(uintptr)
	release             func(uintptr)
}

var axFocus struct {
	once sync.Once
	api  axFocusAPI
	err  error
}

var axFocusCallbacks struct {
	sync.Mutex
	ids      atomic.Uint64
	channels map[uintptr]chan<- struct{}
}

var axFocusCallback = purego.NewCallback(func(_ uintptr, _ uintptr, _ uintptr, refcon uintptr) {
	axFocusCallbacks.Lock()
	defer axFocusCallbacks.Unlock()
	if events := axFocusCallbacks.channels[refcon]; events != nil {
		select {
		case events <- struct{}{}:
		default:
		}
	}
})

// focusStopTimeout bounds how long stop waits for the observer goroutine.
const focusStopTimeout = 2 * time.Second

type focusWatcherStart struct {
	loop uintptr
	err  error
}

func nativeFocusSubscription(pid int32, events chan<- struct{}) (func(), error) {
	if pid <= 0 || events == nil {
		return nil, errors.New("invalid focus observer request")
	}
	axFocus.once.Do(loadAXFocusAPI)
	if axFocus.err != nil {
		return nil, errors.New("accessibility focus observer unavailable")
	}
	refcon := uintptr(axFocusCallbacks.ids.Add(1))
	axFocusCallbacks.Lock()
	if axFocusCallbacks.channels == nil {
		axFocusCallbacks.channels = make(map[uintptr]chan<- struct{})
	}
	axFocusCallbacks.channels[refcon] = events
	axFocusCallbacks.Unlock()

	ready := make(chan focusWatcherStart, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAXFocusObserver(axFocus.api, pid, refcon, ready)
	}()
	started := <-ready
	if started.err != nil {
		<-done
		removeAXFocusCallback(refcon)
		return nil, errors.New("accessibility focus observer registration failed")
	}
	var stop sync.Once
	return func() {
		stop.Do(func() {
			// CFRunLoopStop is a no-op until the loop is running, and ready is
			// signalled just before CFRunLoopRun, so keep stopping until done.
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			timeout := time.NewTimer(focusStopTimeout)
			defer timeout.Stop()
		wait:
			for {
				axFocus.api.runLoopStop(started.loop)
				axFocus.api.runLoopWake(started.loop)
				select {
				case <-done:
					break wait
				case <-timeout.C:
					break wait
				case <-ticker.C:
				}
			}
			removeAXFocusCallback(refcon)
		})
	}, nil
}

func removeAXFocusCallback(refcon uintptr) {
	axFocusCallbacks.Lock()
	delete(axFocusCallbacks.channels, refcon)
	axFocusCallbacks.Unlock()
}

func runAXFocusObserver(api axFocusAPI, pid int32, refcon uintptr, ready chan<- focusWatcherStart) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(objc.RegisterName("alloc"))
	pool = pool.Send(objc.RegisterName("init"))
	if pool == 0 {
		ready <- focusWatcherStart{err: errors.New("autorelease pool unavailable")}
		return
	}
	defer pool.Send(objc.RegisterName("drain"))

	application := api.createApplication(pid)
	if application == 0 {
		ready <- focusWatcherStart{err: errors.New("application element unavailable")}
		return
	}
	defer api.release(application)
	var observer uintptr
	if status := api.observerCreate(pid, axFocusCallback, &observer); status != 0 || observer == 0 {
		ready <- focusWatcherStart{err: errors.New("observer create failed")}
		return
	}
	defer api.release(observer)
	source := api.observerSource(observer)
	loop := api.runLoopCurrent()
	if source == 0 || loop == 0 {
		ready <- focusWatcherStart{err: errors.New("observer run loop unavailable")}
		return
	}
	mode := objc.ID(objc.GetClass("NSString")).Send(objc.RegisterName("stringWithUTF8String:"), "kCFRunLoopDefaultMode")
	if mode == 0 {
		ready <- focusWatcherStart{err: errors.New("default run loop mode unavailable")}
		return
	}
	api.runLoopAddSource(loop, source, uintptr(mode))
	defer api.runLoopRemoveSource(loop, source, uintptr(mode))

	added := false
	for _, name := range []string{"AXFocusedWindowChanged", "AXFocusedUIElementChanged"} {
		notification := objc.ID(objc.GetClass("NSString")).Send(objc.RegisterName("stringWithUTF8String:"), name)
		if notification == 0 {
			continue
		}
		notification.Send(objc.RegisterName("retain"))
		if api.observerAdd(observer, application, uintptr(notification), refcon) == 0 {
			added = true
		}
		api.release(uintptr(notification))
	}
	if !added {
		ready <- focusWatcherStart{err: errors.New("focus notifications unsupported")}
		return
	}
	ready <- focusWatcherStart{loop: loop}
	api.runLoopRun()
}

func loadAXFocusAPI() {
	appServices, err := purego.Dlopen("/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		axFocus.err = errors.New("ApplicationServices unavailable")
		return
	}
	cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		axFocus.err = errors.New("CoreFoundation unavailable")
		return
	}
	a := axFocusAPI{appServices: appServices, coreFoundation: cf}
	purego.RegisterLibFunc(&a.observerCreate, appServices, "AXObserverCreate")
	purego.RegisterLibFunc(&a.observerAdd, appServices, "AXObserverAddNotification")
	purego.RegisterLibFunc(&a.observerSource, appServices, "AXObserverGetRunLoopSource")
	purego.RegisterLibFunc(&a.createApplication, appServices, "AXUIElementCreateApplication")
	purego.RegisterLibFunc(&a.runLoopCurrent, cf, "CFRunLoopGetCurrent")
	purego.RegisterLibFunc(&a.runLoopAddSource, cf, "CFRunLoopAddSource")
	purego.RegisterLibFunc(&a.runLoopRemoveSource, cf, "CFRunLoopRemoveSource")
	purego.RegisterLibFunc(&a.runLoopRun, cf, "CFRunLoopRun")
	purego.RegisterLibFunc(&a.runLoopStop, cf, "CFRunLoopStop")
	purego.RegisterLibFunc(&a.runLoopWake, cf, "CFRunLoopWakeUp")
	purego.RegisterLibFunc(&a.release, cf, "CFRelease")
	axFocus.api = a
}
