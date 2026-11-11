package cgo

import (
	"errors"
	"path/filepath"
	"syscall"
)

// Dummy RTLD flags
const (
	RTLD_DEFAULT = 0
	RTLD_LAZY    = 0
	RTLD_NOW     = 0
	RTLD_LOCAL   = 0
	RTLD_GLOBAL  = 0
)

// Dlopen examines the dynamic library or bundle file specified by path.
//
// The mode was ignored on Windows.
func Dlopen(path string, mode int) (uintptr, error) {
	if len(path) == 0 {
		return 0, errors.New("invalid library path")
	}
	handle, err := syscall.LoadLibrary(filepath.ToSlash(path))
	if err != nil {
		return 0, err
	}
	return uintptr(handle), nil
}

// Dlsym takes a "handle" of a dynamic library returned by Dlopen and the symbol name.
func Dlsym(handle uintptr, name string) (uintptr, error) {
	if handle == 0 {
		return 0, errors.New("invalid handle")
	}
	return syscall.GetProcAddress(syscall.Handle(handle), name)
}

// Dlclose decrements the reference count on the dynamic library handle.
func Dlclose(handle uintptr) error {
	return syscall.FreeLibrary(syscall.Handle(handle))
}

// NewCallback converts a Go function to a C function pointer that can be
// passed to C code as a callback.
//
// On windows/amd64 the full Microsoft x64 calling convention is supported,
// including float32/float64 and struct arguments and return values. Struct
// types must have the same memory layout as their C counterpart (add explicit
// padding fields if needed).
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
	return syscall.SyscallN(fn, args...)
}
