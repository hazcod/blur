// Copyright 2010 The win Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package win

import (
	"golang.org/x/sys/windows"
	"syscall"
)

type CSIDL uint32
type HDROP HANDLE

type NOTIFYICONDATA struct {
	CbSize           uint32
	HWnd             HWND
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            HICON
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         syscall.GUID
	HBalloonIcon     HICON
}

// NOTIFYICONIDENTIFIER is hand-written because shellapi.h declares it with
// #pragma pack(1) on 386. The fields happen to sit at their natural offsets
// there too, so only the alignment differs, which does not matter when the
// struct is passed by pointer.
type NOTIFYICONIDENTIFIER struct {
	CbSize   uint32
	HWnd     HWND
	UID      uint32
	GuidItem syscall.GUID
}

// SHELLEXECUTEINFO is hand-written because the generator does not support
// its anonymous union of hIcon and hMonitor, which HIconOrMonitor stands
// for. shellapi.h declares it with #pragma pack(1) on 386, where every
// field is 4 bytes, so the layout is the same.
type SHELLEXECUTEINFO struct {
	CbSize         uint32
	FMask          uint32
	Hwnd           HWND
	LpVerb         *uint16
	LpFile         *uint16
	LpParameters   *uint16
	LpDirectory    *uint16
	NShow          int32
	HInstApp       HINSTANCE
	LpIDList       uintptr
	LpClass        *uint16
	HkeyClass      HKEY
	DwHotKey       uint32
	HIconOrMonitor HANDLE
	HProcess       HANDLE
}

type SHFILEINFO struct {
	HIcon         HICON
	IIcon         int32
	DwAttributes  uint32
	SzDisplayName [MAX_PATH]uint16
	SzTypeName    [80]uint16
}

var (

	// Functions
	dragAcceptFiles *windows.LazyProc
)

func init() {
	// Functions
	dragAcceptFiles = libshell32.NewProc("DragAcceptFiles")
}

func DragAcceptFiles(hWnd HWND, fAccept bool) bool {
	ret, _, _ := syscall.SyscallN(dragAcceptFiles.Addr(),
		uintptr(hWnd),
		uintptr(BoolToBOOL(fAccept)))

	return ret != 0
}
