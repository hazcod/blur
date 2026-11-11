// Copyright 2010 The win Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows && amd64

package win

import (
	"syscall"
	"unsafe"
)

// SetHwndProp wraps SetPropValue, providing a convenient entry point for callers who are annotating HWND-based accessible elements. If the new value is a string, you can use SetHwndPropStr instead.
func (obj *IAccPropServices) SetHwndProp(hwnd HWND, idObject int32, idChild uint32, idProp *MSAAPROPID, v *VARIANT) HRESULT {
	ret, _, _ := syscall.SyscallN(obj.LpVtbl.SetHwndProp,
		uintptr(unsafe.Pointer(obj)),
		uintptr(hwnd),
		uintptr(idObject),
		uintptr(idChild),
		uintptr(unsafe.Pointer(idProp)),
		uintptr(unsafe.Pointer(v)))
	return HRESULT(ret)
}

// SetHwndPropStr wraps SetPropValue, providing a more convenient entry point for callers who are annotating HWND-based accessible elements.
func (obj *IAccPropServices) SetHwndPropStr(hwnd HWND, idObject int32, idChild uint32, idProp *MSAAPROPID, str string) HRESULT {
	str16, err := syscall.UTF16PtrFromString(str)
	if err != nil {
		return -((E_INVALIDARG ^ 0xFFFFFFFF) + 1)
	}
	ret, _, _ := syscall.SyscallN(obj.LpVtbl.SetHwndPropStr,
		uintptr(unsafe.Pointer(obj)),
		uintptr(hwnd),
		uintptr(idObject),
		uintptr(idChild),
		uintptr(unsafe.Pointer(idProp)),
		uintptr(unsafe.Pointer(str16)))
	return HRESULT(ret)
}
