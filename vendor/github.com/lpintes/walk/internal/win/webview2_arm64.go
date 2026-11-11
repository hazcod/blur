// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package win

import (
	"syscall"
	"unsafe"
)

// Put_Bounds takes a RECT by value. On arm64 a composite of at most 16
// bytes is passed in consecutive integer registers, so the 16-byte RECT
// is passed as two 8-byte arguments (Left and Top, then Right and
// Bottom, each pair in memory order).
func (obj *ICoreWebView2Controller) Put_Bounds(bounds RECT) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Put_Bounds,
		uintptr(unsafe.Pointer(obj)),
		uintptr(uint64(uint32(bounds.Left))|uint64(uint32(bounds.Top))<<32),
		uintptr(uint64(uint32(bounds.Right))|uint64(uint32(bounds.Bottom))<<32))
	return HRESULT(r1)
}

// removeEventHandler calls a Remove_ method, which takes an
// EventRegistrationToken (an int64) by value in one register.
func removeEventHandler(method uintptr, obj unsafe.Pointer, token EventRegistrationToken) HRESULT {
	r1, _, _ := syscall.SyscallN(method, uintptr(obj), uintptr(token.Value))
	return HRESULT(r1)
}
