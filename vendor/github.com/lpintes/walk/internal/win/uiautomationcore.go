// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package win

import (
	"syscall"
	"unsafe"
)

func (obj *IRawElementProviderSimple) Release() uint32 {
	ret, _, _ := syscall.SyscallN(obj.LpVtbl.Release,
		uintptr(unsafe.Pointer(obj)))
	return uint32(ret)
}

// UiaRaiseNotificationEventAvailable reports whether UiaRaiseNotificationEvent
// exists. It was added in Windows 10 version 1709; calling it on older
// versions panics.
func UiaRaiseNotificationEventAvailable() bool {
	return procUiaRaiseNotificationEvent.Find() == nil
}

func (obj *IUIAutomation5) Release() uint32 {
	ret, _, _ := syscall.SyscallN(obj.LpVtbl.Release,
		uintptr(unsafe.Pointer(obj)))
	return uint32(ret)
}

func (obj *IUIAutomation5) AddNotificationEventHandler(element *IUIAutomationElement, scope int32, cacheRequest unsafe.Pointer, handler *IUIAutomationNotificationEventHandler) HRESULT {
	ret, _, _ := syscall.SyscallN(obj.LpVtbl.AddNotificationEventHandler,
		uintptr(unsafe.Pointer(obj)),
		uintptr(unsafe.Pointer(element)),
		uintptr(scope),
		uintptr(cacheRequest),
		uintptr(unsafe.Pointer(handler)))
	return HRESULT(ret)
}

func (obj *IUIAutomation5) RemoveNotificationEventHandler(element *IUIAutomationElement, handler *IUIAutomationNotificationEventHandler) HRESULT {
	ret, _, _ := syscall.SyscallN(obj.LpVtbl.RemoveNotificationEventHandler,
		uintptr(unsafe.Pointer(obj)),
		uintptr(unsafe.Pointer(element)),
		uintptr(unsafe.Pointer(handler)))
	return HRESULT(ret)
}
