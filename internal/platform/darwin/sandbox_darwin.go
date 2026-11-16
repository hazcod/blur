//go:build darwin

package darwin

import (
	"runtime"
	"sync"

	"github.com/ebitengine/purego"
)

// AppSandboxEnabled reads this executable's signed entitlement. An inherited
// HOME or APP_SANDBOX_CONTAINER_ID cannot authorize compatibility migration.
// Unknown/unreadable entitlement state never enables that migration.
func AppSandboxEnabled() bool { return appSandboxEnabled() }

var appSandboxEnabled = sync.OnceValue(func() bool {
	security, err := purego.Dlopen("/System/Library/Frameworks/Security.framework/Security", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return false
	}
	core, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return false
	}
	var taskCreate func(uintptr) uintptr
	var taskValue func(uintptr, uintptr, uintptr) uintptr
	var createString func(uintptr, uintptr, uint32) uintptr
	var getType func(uintptr) uintptr
	var booleanType func() uintptr
	var booleanValue func(uintptr) uint8
	var release func(uintptr)
	purego.RegisterLibFunc(&taskCreate, security, "SecTaskCreateFromSelf")
	purego.RegisterLibFunc(&taskValue, security, "SecTaskCopyValueForEntitlement")
	purego.RegisterLibFunc(&createString, core, "CFStringCreateWithCString")
	purego.RegisterLibFunc(&getType, core, "CFGetTypeID")
	purego.RegisterLibFunc(&booleanType, core, "CFBooleanGetTypeID")
	purego.RegisterLibFunc(&booleanValue, core, "CFBooleanGetValue")
	purego.RegisterLibFunc(&release, core, "CFRelease")
	task := taskCreate(0)
	if task == 0 {
		return false
	}
	defer release(task)
	bytes, pointer := cString("com.apple.security.app-sandbox")
	key := createString(0, pointer, 0x08000100)
	runtime.KeepAlive(bytes)
	if key == 0 {
		return false
	}
	defer release(key)
	value := taskValue(task, key, 0)
	if value == 0 {
		return false
	}
	defer release(value)
	return getType(value) == booleanType() && booleanValue(value) != 0
})
