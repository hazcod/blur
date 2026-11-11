// Copyright 2010 The win Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package win

import (
	"syscall"
	"unsafe"
)

type MSAAPROPID syscall.GUID

const (
	STATE_SYSTEM_VALID = 0x7fffffff
)

func (obj *IAccPropServices) Release() uint32 {
	ret, _, _ := syscall.SyscallN(obj.LpVtbl.Release,
		uintptr(unsafe.Pointer(obj)))
	return uint32(ret)
}

// ClearHwndProps wraps SetPropValue, SetPropServer, and ClearProps, and provides a convenient entry point for callers who are annotating HWND-based accessible elements.
func (obj *IAccPropServices) ClearHwndProps(hwnd HWND, idObject int32, idChild uint32, idProps []MSAAPROPID) HRESULT {
	var idPropsPtr unsafe.Pointer
	idPropsLen := len(idProps)
	if idPropsLen != 0 {
		idPropsPtr = unsafe.Pointer(&idProps[0])
	}
	ret, _, _ := syscall.SyscallN(obj.LpVtbl.ClearHwndProps,
		uintptr(unsafe.Pointer(obj)),
		uintptr(hwnd),
		uintptr(idObject),
		uintptr(idChild),
		uintptr(idPropsPtr),
		uintptr(idPropsLen))
	return HRESULT(ret)
}
