// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package win

import (
	"syscall"
	"unsafe"
)

// Put_Bounds takes a RECT by value. On amd64 a struct larger than 8 bytes
// is passed as a pointer to a copy made by the caller.
func (obj *ICoreWebView2Controller) Put_Bounds(bounds RECT) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Put_Bounds,
		uintptr(unsafe.Pointer(obj)),
		uintptr(unsafe.Pointer(&bounds)))
	return HRESULT(r1)
}

// removeEventHandler calls a Remove_ method, which takes an
// EventRegistrationToken (an int64) by value in one register.
func removeEventHandler(method uintptr, obj unsafe.Pointer, token EventRegistrationToken) HRESULT {
	r1, _, _ := syscall.SyscallN(method, uintptr(obj), uintptr(token.Value))
	return HRESULT(r1)
}
