// Copyright 2010 The win Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package win

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// Predefined window handles
const (
	HWND_NOTOPMOST = ^HWND(1) // -2
	HWND_TOPMOST   = ^HWND(0) // -1
	HWND_MESSAGE   = ^HWND(2) // -3
)

type (
	HACCEL   HANDLE
	HCURSOR  HANDLE
	HDWP     HANDLE
	HICON    HANDLE
	HMENU    HANDLE
	HMONITOR HANDLE
	HWND     HANDLE
)

type CHANGEFILTERSTRUCT struct {
	size      uint32
	extStatus uint32
}

type TPMPARAMS struct {
	CbSize    uint32
	RcExclude RECT
}

func GET_X_LPARAM(lp uintptr) int32 {
	return int32(int16(LOWORD(uint32(lp))))
}

func GET_Y_LPARAM(lp uintptr) int32 {
	return int32(int16(HIWORD(uint32(lp))))
}

var (

	// Functions
	addClipboardFormatListener *windows.LazyProc
	getDpiForWindow            *windows.LazyProc
	getSystemMetrics           *windows.LazyProc
	getSystemMetricsForDpi     *windows.LazyProc
)

func init() {
	// Functions
	addClipboardFormatListener = libuser32.NewProc("AddClipboardFormatListener")
	getDpiForWindow = libuser32.NewProc("GetDpiForWindow")
	getSystemMetrics = libuser32.NewProc("GetSystemMetrics")
	getSystemMetricsForDpi = libuser32.NewProc("GetSystemMetricsForDpi")
}

func AddClipboardFormatListener(hwnd HWND) bool {
	if addClipboardFormatListener.Find() != nil {
		return false
	}

	ret, _, _ := syscall.SyscallN(addClipboardFormatListener.Addr(),
		uintptr(hwnd))

	return ret != 0
}

func GetDpiForWindow(hwnd HWND) uint32 {
	if getDpiForWindow.Find() != nil {
		hdc := GetDC(hwnd)
		defer ReleaseDC(hwnd, hdc)

		return uint32(GetDeviceCaps(hdc, LOGPIXELSY))
	}

	ret, _, _ := syscall.SyscallN(getDpiForWindow.Addr(),
		uintptr(hwnd))

	return uint32(ret)
}

func GetSystemMetrics(nIndex int32) int32 {
	ret, _, _ := syscall.SyscallN(getSystemMetrics.Addr(),
		uintptr(nIndex))

	return int32(ret)
}

func GetSystemMetricsForDpi(nIndex int32, dpi uint32) int32 {
	if getSystemMetricsForDpi.Find() != nil {
		return GetSystemMetrics(nIndex)
	}

	ret, _, _ := syscall.SyscallN(getSystemMetricsForDpi.Addr(),
		uintptr(nIndex),
		uintptr(dpi))

	return int32(ret)
}
