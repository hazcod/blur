// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package win

// SetValue takes a float64, which arm64 passes in a floating-point
// register. syscall.SyscallN sets only the integer registers on arm64, so
// the call cannot be made; SetValue returns E_NOTIMPL.
func (obj *IUIAutomationRangeValuePattern) SetValue(val float64) HRESULT {
	return -((E_NOTIMPL ^ 0xFFFFFFFF) + 1)
}

// SetScrollPercent takes two float64 values and returns E_NOTIMPL for the
// same reason as SetValue.
func (obj *IUIAutomationScrollPattern) SetScrollPercent(horizontalPercent, verticalPercent float64) HRESULT {
	return -((E_NOTIMPL ^ 0xFFFFFFFF) + 1)
}
