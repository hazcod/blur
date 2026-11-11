// Copyright 2016 The win Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package win

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TrackBar (Slider) messages
const (
	TBM_GETPOS = WM_USER
)

const (
	LPSTR_TEXTCALLBACK = ^uintptr(0)
)

type HIMAGELIST HANDLE

var (
	loadIconWithScaleDown *windows.LazyProc
)

func init() {
	// Functions
	loadIconWithScaleDown = libcomctl32.NewProc("LoadIconWithScaleDown")
}

func LoadIconWithScaleDown(hInstance HINSTANCE, lpIconName *uint16, w int32, h int32, hicon *HICON) HRESULT {
	if loadIconWithScaleDown.Find() != nil {
		return HRESULT(0)
	}
	ret, _, _ := syscall.SyscallN(loadIconWithScaleDown.Addr(),
		uintptr(hInstance),
		uintptr(unsafe.Pointer(lpIconName)),
		uintptr(w),
		uintptr(h),
		uintptr(unsafe.Pointer(hicon)))

	return HRESULT(ret)
}

// LoadIconWithScaleDownID is LoadIconWithScaleDown for an integer resource
// ID (MAKEINTRESOURCE in C).
func LoadIconWithScaleDownID(hInstance HINSTANCE, id uintptr, w int32, h int32, hicon *HICON) HRESULT {
	if loadIconWithScaleDown.Find() != nil {
		return HRESULT(0)
	}
	ret, _, _ := syscall.SyscallN(loadIconWithScaleDown.Addr(),
		uintptr(hInstance),
		id,
		uintptr(w),
		uintptr(h),
		uintptr(unsafe.Pointer(hicon)))

	return HRESULT(ret)
}
