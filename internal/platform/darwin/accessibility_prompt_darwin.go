package darwin

import (
	"errors"
	"runtime"
	"sync"

	"github.com/ebitengine/purego"
)

const accessibilityPromptPropertyList = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>AXTrustedCheckOptionPrompt</key><true/></dict></plist>`

var accessibilityPrompt struct {
	once sync.Once
	call func() error
	err  error
}

// nativeAccessibilityPrompt delegates permission guidance to macOS. It does
// not mutate TCC state and is called at most once by the capture controller.
func nativeAccessibilityPrompt() error {
	accessibilityPrompt.once.Do(loadAccessibilityPrompt)
	if accessibilityPrompt.err != nil {
		return accessibilityPrompt.err
	}
	if accessibilityPrompt.call == nil {
		return errors.New("darwin: accessibility permission guidance unavailable")
	}
	return accessibilityPrompt.call()
}

func loadAccessibilityPrompt() {
	applicationServices, err := purego.Dlopen("/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		accessibilityPrompt.err = err
		return
	}
	coreFoundation, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		accessibilityPrompt.err = err
		return
	}

	var check func(uintptr) uint8
	var createData func(uintptr, *byte, int64) uintptr
	var createPropertyList func(uintptr, uintptr, uint64, uintptr, uintptr) uintptr
	var release func(uintptr)
	if err := registerNativeFunction(applicationServices, "AXIsProcessTrustedWithOptions", &check); err != nil {
		accessibilityPrompt.err = err
		return
	}
	if err := registerNativeFunction(coreFoundation, "CFDataCreate", &createData); err != nil {
		accessibilityPrompt.err = err
		return
	}
	if err := registerNativeFunction(coreFoundation, "CFPropertyListCreateWithData", &createPropertyList); err != nil {
		accessibilityPrompt.err = err
		return
	}
	if err := registerNativeFunction(coreFoundation, "CFRelease", &release); err != nil {
		accessibilityPrompt.err = err
		return
	}

	accessibilityPrompt.call = func() error {
		payload := []byte(accessibilityPromptPropertyList)
		data := createData(0, &payload[0], int64(len(payload)))
		if data == 0 {
			return errors.New("darwin: accessibility prompt data unavailable")
		}
		defer release(data)
		options := createPropertyList(0, data, 0, 0, 0)
		if options == 0 {
			return errors.New("darwin: accessibility prompt options unavailable")
		}
		defer release(options)
		_ = check(options) // The return value is only a snapshot; Capability rechecks it.
		runtime.KeepAlive(payload)
		return nil
	}
}

func registerNativeFunction(handle uintptr, name string, function any) error {
	address, err := purego.Dlsym(handle, name)
	if err != nil || address == 0 {
		return errors.New("darwin: accessibility permission API unavailable")
	}
	purego.RegisterFunc(function, address)
	return nil
}
