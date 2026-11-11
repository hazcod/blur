// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build (windows && 386) || (windows && arm)

package win

import (
	"syscall"
	"unsafe"
)

// Put_Bounds takes a RECT by value. On 386 a struct passed by value is
// copied to the stack, so it is passed as one argument per 4 bytes.
func (obj *ICoreWebView2Controller) Put_Bounds(bounds RECT) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Put_Bounds,
		uintptr(unsafe.Pointer(obj)),
		uintptr(bounds.Left),
		uintptr(bounds.Top),
		uintptr(bounds.Right),
		uintptr(bounds.Bottom))
	return HRESULT(r1)
}

// removeEventHandler calls a Remove_ method, which takes an
// EventRegistrationToken (an int64) by value. On 386 it is copied to the
// stack as two 4-byte words, the low one first.
func removeEventHandler(method uintptr, obj unsafe.Pointer, token EventRegistrationToken) HRESULT {
	r1, _, _ := syscall.SyscallN(method,
		uintptr(obj),
		uintptr(uint32(token.Value)),
		uintptr(uint32(token.Value>>32)))
	return HRESULT(r1)
}
