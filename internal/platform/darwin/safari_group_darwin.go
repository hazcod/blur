//go:build darwin

package darwin

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"
)

// These values are supplied by the application's signing/build configuration.
// Empty values keep Safari capture unavailable on unsigned or unconfigured builds.
var SafariAppGroupID string
var SafariKeychainAccessGroup string

var errSafariGroupUnavailable = errors.New("Safari App Group unavailable")

func validSafariAppGroupID(value string) bool {
	if len(value) < 12 || len(value) > 255 || !strings.HasPrefix(value, "group.") || value[len(value)-1] == '.' {
		return false
	}
	segmentStart, segments := 0, 0
	for i := 0; i <= len(value); i++ {
		if i < len(value) && value[i] != '.' {
			c := value[i]
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-') {
				return false
			}
			continue
		}
		if i == segmentStart {
			return false
		}
		segments++
		segmentStart = i + 1
	}
	return segments >= 3
}

// SafariContainerPath asks Foundation for the signed App Group container. It
// never guesses a ~/Library path and never falls back to an ordinary directory.
func SafariContainerPath(groupID string) (string, error) {
	if !validSafariAppGroupID(groupID) {
		return "", errSafariGroupUnavailable
	}
	cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return "", errSafariGroupUnavailable
	}
	objc, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return "", errSafariGroupUnavailable
	}
	if _, err := purego.Dlopen("/System/Library/Frameworks/Foundation.framework/Foundation", purego.RTLD_NOW|purego.RTLD_LOCAL); err != nil {
		return "", errSafariGroupUnavailable
	}
	var createString func(uintptr, uintptr, uint32) uintptr
	var getCString func(uintptr, unsafe.Pointer, int64, uint32) uint8
	var release func(uintptr)
	purego.RegisterLibFunc(&createString, cf, "CFStringCreateWithCString")
	purego.RegisterLibFunc(&getCString, cf, "CFStringGetCString")
	purego.RegisterLibFunc(&release, cf, "CFRelease")
	var getClass func(uintptr) uintptr
	var selector func(uintptr) uintptr
	var send0 func(uintptr, uintptr) uintptr
	var send1 func(uintptr, uintptr, uintptr) uintptr
	purego.RegisterLibFunc(&getClass, objc, "objc_getClass")
	purego.RegisterLibFunc(&selector, objc, "sel_registerName")
	purego.RegisterLibFunc(&send0, objc, "objc_msgSend")
	purego.RegisterLibFunc(&send1, objc, "objc_msgSend")
	groupCString, groupPointer := cString(groupID)
	group := createString(0, groupPointer, 0x08000100)
	runtime.KeepAlive(groupCString)
	if group == 0 {
		return "", errSafariGroupUnavailable
	}
	defer release(group)
	classNamed := func(name string) uintptr {
		b, p := cString(name)
		v := getClass(p)
		runtime.KeepAlive(b)
		return v
	}
	selectorNamed := func(name string) uintptr {
		b, p := cString(name)
		v := selector(p)
		runtime.KeepAlive(b)
		return v
	}
	poolClass := classNamed("NSAutoreleasePool")
	managerClass := classNamed("NSFileManager")
	if poolClass == 0 || managerClass == 0 {
		return "", errSafariGroupUnavailable
	}
	pool := send0(send0(poolClass, selectorNamed("alloc")), selectorNamed("init"))
	if pool == 0 {
		return "", errSafariGroupUnavailable
	}
	defer send0(pool, selectorNamed("drain"))
	manager := send0(managerClass, selectorNamed("defaultManager"))
	if manager == 0 {
		return "", errSafariGroupUnavailable
	}
	container := send1(manager, selectorNamed("containerURLForSecurityApplicationGroupIdentifier:"), group)
	if container == 0 {
		return "", errSafariGroupUnavailable
	}
	pathString := send0(container, selectorNamed("path"))
	if pathString == 0 {
		return "", errSafariGroupUnavailable
	}
	bytes := make([]byte, 4097)
	if getCString(pathString, unsafe.Pointer(&bytes[0]), int64(len(bytes)), 0x08000100) == 0 {
		clear(bytes)
		return "", errSafariGroupUnavailable
	}
	length := 0
	for length < len(bytes) && bytes[length] != 0 {
		length++
	}
	if length == 0 || length == len(bytes) {
		return "", errSafariGroupUnavailable
	}
	path := string(bytes[:length])
	clear(bytes)
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errSafariGroupUnavailable
	}
	return path, nil
}

// SafariSocketPath creates a dedicated private subdirectory in the resolved
// group container. The shared container itself is never chmodded.
func SafariSocketPath(container string) (string, error) {
	if !filepath.IsAbs(container) || filepath.Clean(container) != container {
		return "", errSafariGroupUnavailable
	}
	info, err := os.Lstat(container)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUID(info) {
		return "", errSafariGroupUnavailable
	}
	directory := filepath.Join(container, "b")
	if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", errSafariGroupUnavailable
	}
	info, err = os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || !ownedByCurrentUID(info) {
		return "", errSafariGroupUnavailable
	}
	path := filepath.Join(directory, "s")
	if len(path) > 103 {
		return "", errSafariGroupUnavailable
	}
	return path, nil
}

func ownedByCurrentUID(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}
