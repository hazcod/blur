// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package win

import (
	"math"
	"syscall"
	"unsafe"
)

// SetValue takes a float64, which the generator does not support. On
// amd64 it is passed in XMM1; syscall.SyscallN copies the first four
// arguments to both the integer and the XMM registers.
func (obj *IUIAutomationRangeValuePattern) SetValue(val float64) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.SetValue,
		uintptr(unsafe.Pointer(obj)),
		uintptr(math.Float64bits(val)))
	return HRESULT(r1)
}

// SetScrollPercent takes two float64 values, passed in XMM1 and XMM2 on
// amd64, which syscall.SyscallN sets as described above.
func (obj *IUIAutomationScrollPattern) SetScrollPercent(horizontalPercent, verticalPercent float64) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.SetScrollPercent,
		uintptr(unsafe.Pointer(obj)),
		uintptr(math.Float64bits(horizontalPercent)),
		uintptr(math.Float64bits(verticalPercent)))
	return HRESULT(r1)
}
