//go:build darwin

package client

import (
	"unsafe"

	"github.com/ebitengine/purego"
)

const csDarwinUserTempDir = 65537 // _CS_DARWIN_USER_TEMP_DIR

// userTempDir returns confstr(_CS_DARWIN_USER_TEMP_DIR), e.g.
// /var/folders/xx/yyyy/T/. Unlike $TMPDIR it is the same inside and outside
// the App Sandbox, and the sandbox grants the app read-write access to it.
func userTempDir() string {
	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return ""
	}
	var confstr func(name int32, buf unsafe.Pointer, length uintptr) uintptr
	purego.RegisterLibFunc(&confstr, lib, "confstr")
	var buf [1024]byte
	n := confstr(csDarwinUserTempDir, unsafe.Pointer(&buf[0]), uintptr(len(buf)))
	if n < 2 || n > uintptr(len(buf)) {
		return ""
	}
	return string(buf[:n-1])
}
