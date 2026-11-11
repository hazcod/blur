// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build (windows && 386) || (windows && arm)

package win

import (
	"math"
	"syscall"
	"unsafe"
)

// The methods in this file take a POINT or a VARIANT by value, which the
// generator does not support. On 386 a struct passed by value is copied
// to the stack, so it is passed as one argument per 4 bytes.

func (obj *IUIAutomation) ElementFromPoint(pt POINT, element **IUIAutomationElement) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.ElementFromPoint,
		uintptr(unsafe.Pointer(obj)),
		uintptr(pt.X),
		uintptr(pt.Y),
		uintptr(unsafe.Pointer(element)))
	return HRESULT(r1)
}

func (obj *IUIAutomation) CreatePropertyCondition(propertyId int32, value *VARIANT, newCondition **IUIAutomationCondition) HRESULT {
	varA, varB, varC, varD := value.split()
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.CreatePropertyCondition,
		uintptr(unsafe.Pointer(obj)),
		uintptr(propertyId),
		varA, varB, varC, varD,
		uintptr(unsafe.Pointer(newCondition)))
	return HRESULT(r1)
}

func (obj *IUIAutomation) CreatePropertyConditionEx(propertyId int32, value *VARIANT, flags int32, newCondition **IUIAutomationCondition) HRESULT {
	varA, varB, varC, varD := value.split()
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.CreatePropertyConditionEx,
		uintptr(unsafe.Pointer(obj)),
		uintptr(propertyId),
		varA, varB, varC, varD,
		uintptr(flags),
		uintptr(unsafe.Pointer(newCondition)))
	return HRESULT(r1)
}

// SetValue passes the float64 as two stack words.
func (obj *IUIAutomationRangeValuePattern) SetValue(val float64) HRESULT {
	bits := math.Float64bits(val)
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.SetValue,
		uintptr(unsafe.Pointer(obj)),
		uintptr(uint32(bits)),
		uintptr(uint32(bits>>32)))
	return HRESULT(r1)
}

// SetScrollPercent passes each float64 as two stack words.
func (obj *IUIAutomationScrollPattern) SetScrollPercent(horizontalPercent, verticalPercent float64) HRESULT {
	h := math.Float64bits(horizontalPercent)
	v := math.Float64bits(verticalPercent)
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.SetScrollPercent,
		uintptr(unsafe.Pointer(obj)),
		uintptr(uint32(h)),
		uintptr(uint32(h>>32)),
		uintptr(uint32(v)),
		uintptr(uint32(v>>32)))
	return HRESULT(r1)
}
