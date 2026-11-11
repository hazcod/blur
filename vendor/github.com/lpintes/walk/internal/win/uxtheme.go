// Copyright 2010 The win Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package win

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type HTHEME HANDLE

var (
	drawThemeTextEx *windows.LazyProc
)

func init() {
	// Functions
	drawThemeTextEx = libuxtheme.NewProc("DrawThemeTextEx")
}

func DrawThemeTextEx(hTheme HTHEME, hdc HDC, iPartId, iStateId int32, pszText *uint16, iCharCount int32, dwFlags uint32, pRect *RECT, pOptions *DTTOPTS) HRESULT {
	if drawThemeTextEx.Find() != nil {
		return HRESULT(0)
	}
	ret, _, _ := syscall.SyscallN(drawThemeTextEx.Addr(),
		uintptr(hTheme),
		uintptr(hdc),
		uintptr(iPartId),
		uintptr(iStateId),
		uintptr(unsafe.Pointer(pszText)),
		uintptr(iCharCount),
		uintptr(dwFlags),
		uintptr(unsafe.Pointer(pRect)),
		uintptr(unsafe.Pointer(pOptions)))

	return HRESULT(ret)
}
