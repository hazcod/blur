// Copyright 2010 The win Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package win

import (
	"golang.org/x/sys/windows"
)

const (
	E_NOTIMPL     = 0x80004001
	E_INVALIDARG  = 0x80070057
	E_NOINTERFACE = 0x80004002
	E_POINTER     = 0x80004003
)

type (
	BOOL    int32
	HRESULT int32
)

func SUCCEEDED(hr HRESULT) bool {
	return hr >= 0
}

func FAILED(hr HRESULT) bool {
	return hr < 0
}

// HRESULT_FROM_WIN32 converts a Win32 error code to an HRESULT.
func HRESULT_FROM_WIN32(x uint32) HRESULT {
	if int32(x) <= 0 {
		return HRESULT(int32(x))
	}
	const facilityWin32 = 7
	return HRESULT(int32(x&0xffff | facilityWin32<<16 | 0x80000000))
}

func MAKEWORD(lo, hi byte) uint16 {
	return uint16(uint16(lo) | ((uint16(hi)) << 8))
}

func MAKELONG(lo, hi uint16) uint32 {
	return uint32(uint32(lo) | ((uint32(hi)) << 16))
}

func LOWORD(dw uint32) uint16 {
	return uint16(dw)
}

func HIWORD(dw uint32) uint16 {
	return uint16(dw >> 16 & 0xffff)
}

func UTF16PtrToString(s *uint16) string {
	return windows.UTF16PtrToString(s)
}

// StringToUTF16 returns the UTF-16 encoding of s with a terminating NUL
// added. Like the deprecated syscall.StringToUTF16, it panics if s
// contains a NUL byte; use windows.UTF16FromString to get an error
// instead.
func StringToUTF16(s string) []uint16 {
	a, err := windows.UTF16FromString(s)
	if err != nil {
		panic("syscall: string with NUL passed to StringToUTF16")
	}
	return a
}

// StringToUTF16Ptr returns a pointer to the UTF-16 encoding of s with a
// terminating NUL added. Like the deprecated syscall.StringToUTF16Ptr, it
// panics if s contains a NUL byte; use windows.UTF16PtrFromString to get
// an error instead.
func StringToUTF16Ptr(s string) *uint16 {
	return &StringToUTF16(s)[0]
}

// There is no MAKEINTRESOURCE: a resource ID in a *uint16 or unsafe.Pointer
// is a pointer below 4096 to the Go runtime, which aborts with "invalid
// pointer found on stack" when it finds one while copying a goroutine
// stack. Functions taking a resource name have a variant ending in ID that
// takes the ID as a uintptr (LoadIconID, LoadCursorID, LoadImageID,
// FindResourceID, LoadIconWithScaleDownID).

func BoolToBOOL(value bool) BOOL {
	if value {
		return 1
	}

	return 0
}
