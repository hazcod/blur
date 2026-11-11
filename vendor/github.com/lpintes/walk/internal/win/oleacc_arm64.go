// Copyright 2010 The win Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows && arm64

package win

import (
	"syscall"
	"unsafe"
)

func (idProp *MSAAPROPID) split() (uintptr, uintptr) {
	if idProp == nil {
		return 0, 0
	}
	x := (*struct{ a, b uintptr })(unsafe.Pointer(idProp))
	return x.a, x.b
}

// SetHwndProp wraps SetPropValue, providing a convenient entry point for callers who are annotating HWND-based accessible elements. If the new value is a string, you can use SetHwndPropStr instead.
func (obj *IAccPropServices) SetHwndProp(hwnd HWND, idObject int32, idChild uint32, idProp *MSAAPROPID, v *VARIANT) HRESULT {
	propA, propB := idProp.split()
	ret, _, _ := syscall.SyscallN(obj.LpVtbl.SetHwndProp,
		uintptr(unsafe.Pointer(obj)),
		uintptr(hwnd),
		uintptr(idObject),
		uintptr(idChild),
		propA, propB,
		uintptr(unsafe.Pointer(v)))
	return HRESULT(ret)
}

// SetHwndPropStr wraps SetPropValue, providing a more convenient entry point for callers who are annotating HWND-based accessible elements.
func (obj *IAccPropServices) SetHwndPropStr(hwnd HWND, idObject int32, idChild uint32, idProp *MSAAPROPID, str string) HRESULT {
	str16, err := syscall.UTF16PtrFromString(str)
	if err != nil {
		return -((E_INVALIDARG ^ 0xFFFFFFFF) + 1)
	}
	propA, propB := idProp.split()
	ret, _, _ := syscall.SyscallN(obj.LpVtbl.SetHwndPropStr,
		uintptr(unsafe.Pointer(obj)),
		uintptr(hwnd),
		uintptr(idObject),
		uintptr(idChild),
		propA, propB,
		uintptr(unsafe.Pointer(str16)))
	return HRESULT(ret)
}
