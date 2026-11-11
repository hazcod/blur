// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build (windows && amd64) || (windows && arm64)

package win

import (
	"syscall"
	"unsafe"
)

// The methods in this file take a POINT or a VARIANT by value, which the
// generator does not support. On amd64 and arm64 the 8-byte POINT is
// passed in one integer register, and the 24-byte VARIANT is passed as a
// pointer to a copy made by the caller, which the callee may change.

func (obj *IUIAutomation) ElementFromPoint(pt POINT, element **IUIAutomationElement) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.ElementFromPoint,
		uintptr(unsafe.Pointer(obj)),
		uintptr(*(*uint64)(unsafe.Pointer(&pt))),
		uintptr(unsafe.Pointer(element)))
	return HRESULT(r1)
}

func (obj *IUIAutomation) CreatePropertyCondition(propertyId int32, value *VARIANT, newCondition **IUIAutomationCondition) HRESULT {
	v := *value
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.CreatePropertyCondition,
		uintptr(unsafe.Pointer(obj)),
		uintptr(propertyId),
		uintptr(unsafe.Pointer(&v)),
		uintptr(unsafe.Pointer(newCondition)))
	return HRESULT(r1)
}

func (obj *IUIAutomation) CreatePropertyConditionEx(propertyId int32, value *VARIANT, flags int32, newCondition **IUIAutomationCondition) HRESULT {
	v := *value
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.CreatePropertyConditionEx,
		uintptr(unsafe.Pointer(obj)),
		uintptr(propertyId),
		uintptr(unsafe.Pointer(&v)),
		uintptr(flags),
		uintptr(unsafe.Pointer(newCondition)))
	return HRESULT(r1)
}
