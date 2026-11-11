// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package win

import (
	"runtime"
	"unsafe"
)

// The predefined icons of a task dialog: MAKEINTRESOURCEW(-1) to (-4), which
// cut the value to a WORD.
const (
	TD_WARNING_ICON     = 0xFFFF
	TD_ERROR_ICON       = 0xFFFE
	TD_INFORMATION_ICON = 0xFFFD
	TD_SHIELD_ICON      = 0xFFFC
)

// TASKDIALOG_BUTTON and TASKDIALOGCONFIG are hand-written because commctrl.h
// declares them with #pragma pack(1). On 64-bit the pointer fields then sit
// at offsets that are not multiples of 8 (hwndParent at 4, for example),
// which Go cannot express with plain fields. The Go structs below have the
// fields of the C structs in their natural Go layout; TaskDialogIndirect
// packs them into memory laid out as the C structs before the call.

// TASKDIALOG_BUTTON is a custom button or radio button of a task dialog.
type TASKDIALOG_BUTTON struct {
	NButtonID     int32
	PszButtonText *uint16
}

// TASKDIALOGCONFIG describes a task dialog. The fields follow the C struct,
// except that Buttons and RadioButtons stand for the pairs cButtons and
// pButtons and cRadioButtons and pRadioButtons, and cbSize, which
// TaskDialogIndirect fills in. MainIcon and FooterIcon are the unions of
// an HICON and a PCWSTR (TD_*_ICON or MAKEINTRESOURCE).
type TASKDIALOGCONFIG struct {
	HwndParent              HWND
	HInstance               HINSTANCE
	DwFlags                 uint32
	DwCommonButtons         uint32
	PszWindowTitle          *uint16
	MainIcon                uintptr
	PszMainInstruction      *uint16
	PszContent              *uint16
	Buttons                 []TASKDIALOG_BUTTON
	NDefaultButton          int32
	RadioButtons            []TASKDIALOG_BUTTON
	NDefaultRadioButton     int32
	PszVerificationText     *uint16
	PszExpandedInformation  *uint16
	PszExpandedControlText  *uint16
	PszCollapsedControlText *uint16
	FooterIcon              uintptr
	PszFooter               *uint16
	PfCallback              uintptr
	LpCallbackData          uintptr
	CxWidth                 uint32
}

// The sizes of the packed C structs: TASKDIALOGCONFIG has 8 fields of 4
// bytes and 16 pointer-sized fields, TASKDIALOG_BUTTON one of each.
// taskdialog_32.go and taskdialog_64.go check them against the sizes of
// the SDK headers at compile time.
const (
	sizeofTASKDIALOGCONFIG  = 8*4 + 16*ptrSize
	sizeofTASKDIALOG_BUTTON = 4 + ptrSize
)

// packer writes fields one after another, with no padding, as #pragma
// pack(1) lays them out. Windows on 386, amd64 and arm64 is little-endian.
type packer struct {
	buf []byte
	off int
}

func (p *packer) u32(v uint32) {
	for i := range 4 {
		p.buf[p.off+i] = byte(v >> (8 * i))
	}
	p.off += 4
}

func (p *packer) ptr(v uintptr) {
	for i := range int(ptrSize) {
		p.buf[p.off+i] = byte(v >> (8 * i))
	}
	p.off += int(ptrSize)
}

func packButtons(pinner *runtime.Pinner, buttons []TASKDIALOG_BUTTON) uintptr {
	if len(buttons) == 0 {
		return 0
	}
	p := packer{buf: make([]byte, len(buttons)*int(sizeofTASKDIALOG_BUTTON))}
	pinner.Pin(&p.buf[0])
	for _, b := range buttons {
		p.u32(uint32(b.NButtonID))
		p.ptr(uintptr(unsafe.Pointer(b.PszButtonText)))
	}
	return uintptr(unsafe.Pointer(&p.buf[0]))
}

// TaskDialogIndirect packs config as commctrl.h lays out TASKDIALOGCONFIG
// and calls TaskDialogIndirect of comctl32 version 6 (see
// TaskDialogIndirectAvailable). The strings config points to must be Go
// heap memory (as from StringToUTF16Ptr) or memory Windows does not move;
// the packed copies and the results are in pinned heap memory for the
// duration of the call, because the callback runs on the calling
// goroutine, whose stack may move. The results are copied to pnButton,
// pnRadioButton and pfVerificationFlagChecked (each may be nil) after the
// call.
func TaskDialogIndirect(config *TASKDIALOGCONFIG, pnButton, pnRadioButton *int32, pfVerificationFlagChecked *BOOL) HRESULT {
	var pinner runtime.Pinner
	defer pinner.Unpin()

	p := packer{buf: make([]byte, sizeofTASKDIALOGCONFIG)}
	pinner.Pin(&p.buf[0])
	p.u32(uint32(sizeofTASKDIALOGCONFIG))
	p.ptr(uintptr(config.HwndParent))
	p.ptr(uintptr(config.HInstance))
	p.u32(config.DwFlags)
	p.u32(config.DwCommonButtons)
	p.ptr(uintptr(unsafe.Pointer(config.PszWindowTitle)))
	p.ptr(config.MainIcon)
	p.ptr(uintptr(unsafe.Pointer(config.PszMainInstruction)))
	p.ptr(uintptr(unsafe.Pointer(config.PszContent)))
	p.u32(uint32(len(config.Buttons)))
	p.ptr(packButtons(&pinner, config.Buttons))
	p.u32(uint32(config.NDefaultButton))
	p.u32(uint32(len(config.RadioButtons)))
	p.ptr(packButtons(&pinner, config.RadioButtons))
	p.u32(uint32(config.NDefaultRadioButton))
	p.ptr(uintptr(unsafe.Pointer(config.PszVerificationText)))
	p.ptr(uintptr(unsafe.Pointer(config.PszExpandedInformation)))
	p.ptr(uintptr(unsafe.Pointer(config.PszExpandedControlText)))
	p.ptr(uintptr(unsafe.Pointer(config.PszCollapsedControlText)))
	p.ptr(config.FooterIcon)
	p.ptr(uintptr(unsafe.Pointer(config.PszFooter)))
	p.ptr(config.PfCallback)
	p.ptr(config.LpCallbackData)
	p.u32(config.CxWidth)
	if p.off != len(p.buf) {
		panic("win: TASKDIALOGCONFIG packed into the wrong size")
	}

	// Windows writes the results through these pointers, possibly while
	// the callback runs Go code that grows and moves the goroutine stack,
	// so they point to pinned heap memory, not to the caller's variables.
	out := new(struct {
		button, radio int32
		checked       BOOL
	})
	pinner.Pin(out)
	var outButton, outRadio *int32
	var outChecked *BOOL
	if pnButton != nil {
		outButton = &out.button
	}
	if pnRadioButton != nil {
		outRadio = &out.radio
	}
	if pfVerificationFlagChecked != nil {
		outChecked = &out.checked
	}

	hr := taskDialogIndirect(unsafe.Pointer(&p.buf[0]), outButton, outRadio, outChecked)
	runtime.KeepAlive(config)
	if pnButton != nil {
		*pnButton = out.button
	}
	if pnRadioButton != nil {
		*pnRadioButton = out.radio
	}
	if pfVerificationFlagChecked != nil {
		*pfVerificationFlagChecked = out.checked
	}
	return hr
}

// TaskDialogIndirectAvailable reports whether TaskDialogIndirect exists. Only
// version 6 of comctl32.dll exports it, which a program gets with a
// manifest that asks for it (Microsoft.Windows.Common-Controls 6.0);
// calling it without panics.
func TaskDialogIndirectAvailable() bool {
	return procTaskDialogIndirect.Find() == nil
}
