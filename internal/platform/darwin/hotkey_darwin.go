package darwin

import (
	"errors"
	"log/slog"
	"sync"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/hazcod/blur/internal/hotkey"
)

// ErrHotkeyUnavailable means the shortcut could not be registered, usually
// because another app already owns it.
var ErrHotkeyUnavailable = errors.New("darwin: global shortcut unavailable")

const (
	carbonPath         = "/System/Library/Frameworks/Carbon.framework/Carbon"
	hiToolboxPath      = "/System/Library/Frameworks/Carbon.framework/Frameworks/HIToolbox.framework/HIToolbox"
	eventClassKeyboard = 0x6B657962 // 'keyb'
	eventHotKeyPressed = 5
	hotkeySignature    = 0x424C5552 // 'BLUR'
	hotkeyIdentifier   = 1
)

type eventTypeSpec struct{ eventClass, eventKind uint32 }

var quickHotkey struct {
	mu        sync.Mutex
	loaded    bool
	loadErr   error
	register  func(keyCode, modifiers uint32, id uint64, target uintptr, options uint32, out *uintptr) int32
	unregist  func(ref uintptr) int32
	target    func() uintptr
	install   func(target, handler uintptr, count uint32, types *eventTypeSpec, userData uintptr, out *uintptr) int32
	handlerOK bool
	handlerFn uintptr
	ref       uintptr
	onPress   func()
}

func loadHotkeyAPI() error {
	if quickHotkey.loaded {
		return quickHotkey.loadErr
	}
	quickHotkey.loaded = true
	lib, err := purego.Dlopen(carbonPath, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		lib, err = purego.Dlopen(hiToolboxPath, purego.RTLD_NOW|purego.RTLD_LOCAL)
	}
	if err != nil {
		quickHotkey.loadErr = ErrHotkeyUnavailable
		return quickHotkey.loadErr
	}
	for _, name := range []string{"RegisterEventHotKey", "UnregisterEventHotKey", "GetApplicationEventTarget", "InstallEventHandler"} {
		if _, err := purego.Dlsym(lib, name); err != nil {
			quickHotkey.loadErr = ErrHotkeyUnavailable
			return quickHotkey.loadErr
		}
	}
	purego.RegisterLibFunc(&quickHotkey.register, lib, "RegisterEventHotKey")
	purego.RegisterLibFunc(&quickHotkey.unregist, lib, "UnregisterEventHotKey")
	purego.RegisterLibFunc(&quickHotkey.target, lib, "GetApplicationEventTarget")
	purego.RegisterLibFunc(&quickHotkey.install, lib, "InstallEventHandler")
	return nil
}

// RegisterQuickAskHotkey registers spec (see internal/hotkey) as the single
// global shortcut and calls onPress on the AppKit main thread when it fires.
// An empty spec removes the shortcut. Re-registering replaces the previous
// one; on failure no shortcut is left registered. Call on the main thread.
func RegisterQuickAskHotkey(spec string, onPress func()) error {
	quickHotkey.mu.Lock()
	defer quickHotkey.mu.Unlock()
	unregisterQuickAskHotkeyLocked()
	if spec == "" {
		return nil
	}
	code, modifiers, ok := hotkey.Parse(spec)
	if !ok || onPress == nil {
		return ErrHotkeyUnavailable
	}
	if err := loadHotkeyAPI(); err != nil {
		return err
	}
	if !quickHotkey.handlerOK {
		quickHotkey.handlerFn = purego.NewCallback(func(_, _, _ uintptr) uintptr {
			quickHotkey.mu.Lock()
			press := quickHotkey.onPress
			quickHotkey.mu.Unlock()
			if press != nil {
				press()
			}
			return 0 // noErr: handled
		})
		types := eventTypeSpec{eventClass: eventClassKeyboard, eventKind: eventHotKeyPressed}
		var handlerRef uintptr
		if quickHotkey.install(quickHotkey.target(), quickHotkey.handlerFn, 1, &types, 0, &handlerRef) != 0 {
			slog.Warn("global shortcut handler unavailable", "result", "failed")
			return ErrHotkeyUnavailable
		}
		quickHotkey.handlerOK = true
	}
	var ref uintptr
	id := uint64(hotkeySignature) | uint64(hotkeyIdentifier)<<32
	if status := quickHotkey.register(code, modifiers, id, quickHotkey.target(), 0, &ref); status != 0 || ref == 0 {
		slog.Warn("global shortcut could not be registered", "result", "failed")
		return ErrHotkeyUnavailable
	}
	quickHotkey.ref = ref
	quickHotkey.onPress = onPress
	return nil
}

// UnregisterQuickAskHotkey removes the global shortcut, if any.
func UnregisterQuickAskHotkey() {
	quickHotkey.mu.Lock()
	defer quickHotkey.mu.Unlock()
	unregisterQuickAskHotkeyLocked()
}

func unregisterQuickAskHotkeyLocked() {
	if quickHotkey.ref != 0 && quickHotkey.unregist != nil {
		quickHotkey.unregist(quickHotkey.ref)
	}
	quickHotkey.ref = 0
	quickHotkey.onPress = nil
}

// ActivateApplication brings Blur to the foreground so a window opened from a
// global shortcut can take keyboard focus. Call on the AppKit main thread.
func ActivateApplication() {
	app := objc.Send[objc.ID](objc.ID(objc.GetClass("NSApplication")), objc.RegisterName("sharedApplication"))
	if app != 0 {
		objc.Send[objc.ID](app, objc.RegisterName("activateIgnoringOtherApps:"), true)
	}
}
