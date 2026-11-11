// Copyright 2010 The win Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build (windows && 386) || (windows && arm)

package win

import (
	"syscall"
	"unsafe"
)

func (idProp *MSAAPROPID) split() (uintptr, uintptr, uintptr, uintptr) {
	if idProp == nil {
		return 0, 0, 0, 0
	}
	x := (*struct{ a, b, c, d uintptr })(unsafe.Pointer(idProp))
	return x.a, x.b, x.c, x.d
}

// split returns the words of v, which SetHwndProp passes by value on the
// stack: the 16-byte VARIANT takes four 32-bit stack slots.
func (v *VARIANT) split() (uintptr, uintptr, uintptr, uintptr) {
	var w [4]uintptr
	if v != nil {
		*(*VARIANT)(unsafe.Pointer(&w)) = *v
	}
	return w[0], w[1], w[2], w[3]
}

// SetHwndProp wraps SetPropValue, providing a convenient entry point for callers who are annotating HWND-based accessible elements. If the new value is a string, you can use SetHwndPropStr instead.
func (obj *IAccPropServices) SetHwndProp(hwnd HWND, idObject int32, idChild uint32, idProp *MSAAPROPID, v *VARIANT) HRESULT {
	propA, propB, propC, propD := idProp.split()
	varA, varB, varC, varD := v.split()
	ret, _, _ := syscall.SyscallN(obj.LpVtbl.SetHwndProp,
		uintptr(unsafe.Pointer(obj)),
		uintptr(hwnd),
		uintptr(idObject),
		uintptr(idChild),
		propA, propB, propC, propD,
		varA, varB, varC, varD)
	return HRESULT(ret)
}

// SetHwndPropStr wraps SetPropValue, providing a more convenient entry point for callers who are annotating HWND-based accessible elements.
func (obj *IAccPropServices) SetHwndPropStr(hwnd HWND, idObject int32, idChild uint32, idProp *MSAAPROPID, str string) HRESULT {
	str16, err := syscall.UTF16PtrFromString(str)
	if err != nil {
		return -((E_INVALIDARG ^ 0xFFFFFFFF) + 1)
	}
	propA, propB, propC, propD := idProp.split()
	ret, _, _ := syscall.SyscallN(obj.LpVtbl.SetHwndPropStr,
		uintptr(unsafe.Pointer(obj)),
		uintptr(hwnd),
		uintptr(idObject),
		uintptr(idChild),
		propA, propB, propC, propD,
		uintptr(unsafe.Pointer(str16)))
	return HRESULT(ret)
}
