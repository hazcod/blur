package cgo

import "unsafe"

// Length Get the length of an array ending with zero
func Length[T comparable](p unsafe.Pointer) (n int) {
	var zero T
	step := unsafe.Sizeof(zero)
	for {
		pv := (*T)(unsafe.Pointer(uintptr(p) + uintptr(n)*step))
		if *pv == zero {
			break
		}
		n++
	}
	return
}

func Strlen(s unsafe.Pointer) (n int) {
	return Length[byte](s)
}

func GoSliceTemp[T comparable](p unsafe.Pointer) []T {
	return GoSliceNTemp[T](p, Length[T](p))
}

func GoSliceNTemp[T any](p unsafe.Pointer, n int) []T {
	return unsafe.Slice((*T)(p), n)
}

func GoStringTemp(s unsafe.Pointer) string {
	return GoStringNTemp(s, Strlen(s))
}

func GoStringNTemp(s unsafe.Pointer, n int) string {
	return unsafe.String((*byte)(s), n)
}

func GoSlice[T comparable](p unsafe.Pointer) []T {
	return GoSliceN[T](p, Length[T](p))
}

func GoSliceN[T any](p unsafe.Pointer, n int) []T {
	slice := GoSliceNTemp[T](p, n)
	return append(make([]T, 0, n), slice...)
}

func GoString(s unsafe.Pointer) string {
	return GoStringN(s, Strlen(s))
}

func GoStringN(s unsafe.Pointer, n int) string {
	return string(GoSliceNTemp[byte](s, n))
}

func CSlice[S ~[]E, E any](s S) unsafe.Pointer {
	return unsafe.Pointer(unsafe.SliceData(s))
}

func GoBool[T comparable](v T) bool {
	var zero T
	return v != zero
}

// CString converts a Go string to a NUL-terminated C string allocated on the
// C heap with Malloc. The caller owns the memory and must release it with
// Free once it is no longer needed. A fresh copy is always made, even when s
// already ends with a NUL byte. CString("") returns a pointer to a single NUL
// byte. It panics when the allocation fails.
func CString(s string) unsafe.Pointer {
	p := Malloc(Sizet(len(s) + 1))
	if p == nil {
		panic("cgo.CString: out of memory")
	}
	buf := GoSliceNTemp[byte](p, len(s)+1)
	copy(buf, s)
	buf[len(s)] = 0
	return p
}

// CStringTemp returns a borrowed pointer to a NUL-terminated copy of s backed
// by Go memory. It is valid only while s is kept alive (e.g. with
// runtime.KeepAlive) and must not be freed. When s already ends with a NUL
// byte the pointer aliases s's data without copying; otherwise a temporary Go
// buffer is allocated. CStringTemp("") returns nil.
func CStringTemp(s string) unsafe.Pointer {
	if len(s) == 0 {
		return nil
	}
	if s[len(s)-1] == 0 {
		return unsafe.Pointer(unsafe.StringData(s))
	}
	buf := make([]byte, len(s)+1)
	copy(buf, s)
	return CSlice(buf)
}

func CBool(b bool) int8 {
	if b {
		return 1
	}
	return 0
}

func GoPointer[T any](p uintptr) *T {
	return (*T)(Pointer(p))
}
