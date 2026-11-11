//go:build !windows

package cgo

import (
	"github.com/ebitengine/purego"
	"syscall"
)

const (
	RTLD_DEFAULT = purego.RTLD_DEFAULT
	RTLD_LAZY    = purego.RTLD_LAZY
	RTLD_NOW     = purego.RTLD_NOW
	RTLD_LOCAL   = purego.RTLD_LOCAL
	RTLD_GLOBAL  = purego.RTLD_GLOBAL
)

// Dlopen examines the dynamic library or bundle file specified by path.
//
// The mode was ignored on Windows.
func Dlopen(path string, mode int) (uintptr, error) {
	return purego.Dlopen(path, mode)
}

// Dlsym takes a "handle" of a dynamic library returned by Dlopen and the symbol name.
func Dlsym(handle uintptr, name string) (uintptr, error) {
	return purego.Dlsym(handle, name)
}

// Dlclose decrements the reference count on the dynamic library handle.
func Dlclose(handle uintptr) error {
	return purego.Dlclose(handle)
}

// NewCallback converts a Go function to a C function pointer that can be
// passed to C code as a callback.
//
// On linux/amd64, linux/arm64, darwin/amd64 and darwin/arm64 the full C
// calling convention is supported, including float32/float64 and struct
// arguments and return values, following the System V AMD64 and AAPCS64
// ABIs. Struct types must have the same memory layout as their C counterpart
// (add explicit padding fields if needed).
//
// Only a limited number of callbacks may be created in a single Go process,
// and any memory allocated for these callbacks is never released.
func NewCallback(fn any) uintptr {
	return newCallback(fn)
}

// CallRaw calls a native function synchronously. Convert Go pointers to uintptr
// directly in this call's arguments; they remain live and address-stable until
// it returns. Stored uintptr values and addresses retained by C after return
// require explicit ownership/lifetime management by the caller.
//
//go:uintptrescapes
func CallRaw(fn uintptr, args ...uintptr) (r1, r2 uintptr, err syscall.Errno) {
	r1, r2, e := purego.SyscallN(fn, args...)
	return r1, r2, syscall.Errno(e)
}
