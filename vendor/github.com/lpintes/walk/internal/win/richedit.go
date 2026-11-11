// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package win

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// MSFTEDIT_CLASS is the window class of the RichEdit control of msftedit.dll.
const MSFTEDIT_CLASS = "RICHEDIT50W"

var libmsftedit = windows.NewLazySystemDLL("msftedit.dll")

// LoadMsftedit loads msftedit.dll, which registers MSFTEDIT_CLASS.
func LoadMsftedit() error {
	return libmsftedit.Load()
}

// richedit.h declares its structs with #pragma pack(4). The structs below
// are hand-written: CHARFORMAT2 and PARAFORMAT2 have anonymous unions,
// which the generator does not support, but no field is larger than 4
// bytes, so their layout is the natural one. TEXTRANGE and FINDTEXTEX
// have their fields at the natural offsets too; only their alignment is
// 4 instead of 8 on 64-bit, which does not matter when they are passed by
// pointer. EDITSTREAM and ENLINK have pointer-sized fields at offsets Go
// cannot express on 64-bit, so they are arrays of 4-byte words with
// accessors.

// CHARFORMAT2 is CHARFORMAT2W. DwCookie is the union of dwReserved and
// dwCookie.
type CHARFORMAT2 struct {
	CbSize          uint32
	DwMask          uint32
	DwEffects       uint32
	YHeight         int32
	YOffset         int32
	CrTextColor     COLORREF
	BCharSet        byte
	BPitchAndFamily byte
	SzFaceName      [LF_FACESIZE]uint16
	WWeight         uint16
	SSpacing        int16
	CrBackColor     COLORREF
	Lcid            uint32
	DwCookie        uint32
	SStyle          int16
	WKerning        uint16
	BUnderlineType  byte
	BAnimation      byte
	BRevAuthor      byte
	BUnderlineColor byte
}

// PARAFORMAT2 is PARAFORMAT2. WEffects is the union of wReserved and
// wEffects.
type PARAFORMAT2 struct {
	CbSize           uint32
	DwMask           uint32
	WNumbering       uint16
	WEffects         uint16
	DxStartIndent    int32
	DxRightIndent    int32
	DxOffset         int32
	WAlignment       uint16
	CTabCount        int16
	RgxTabs          [MAX_TAB_STOPS]int32
	DySpaceBefore    int32
	DySpaceAfter     int32
	DyLineSpacing    int32
	SStyle           int16
	BLineSpacingRule byte
	BOutlineLevel    byte
	WShadingWeight   uint16
	WShadingStyle    uint16
	WNumberingStart  uint16
	WNumberingStyle  uint16
	WNumberingTab    uint16
	WBorderSpace     uint16
	WBorderWidth     uint16
	WBorders         uint16
}

const (
	LF_FACESIZE   = 32
	MAX_TAB_STOPS = 32
)

// Sizes from the Windows SDK; the build fails if the Go structs differ.
var (
	_ [unsafe.Sizeof(CHARFORMAT2{}) - 116]byte
	_ [116 - unsafe.Sizeof(CHARFORMAT2{})]byte
	_ [unsafe.Sizeof(PARAFORMAT2{}) - 188]byte
	_ [188 - unsafe.Sizeof(PARAFORMAT2{})]byte
	_ [unsafe.Offsetof(CHARFORMAT2{}.CrBackColor) - 96]byte
	_ [96 - unsafe.Offsetof(CHARFORMAT2{}.CrBackColor)]byte
)

// TEXTRANGE is TEXTRANGEW.
type TEXTRANGE struct {
	Chrg      CHARRANGE
	LpstrText *uint16
}

// FINDTEXTEX is FINDTEXTEXW.
type FINDTEXTEX struct {
	Chrg      CHARRANGE
	LpstrText *uint16
	ChrgText  CHARRANGE
}

const ptrSize = unsafe.Sizeof(uintptr(0))

func getPtr(w []uint32) uintptr {
	v := uintptr(w[0])
	if ptrSize == 8 {
		v |= uintptr(uint64(w[1]) << 32)
	}
	return v
}

func setPtr(w []uint32, v uintptr) {
	w[0] = uint32(v)
	if ptrSize == 8 {
		w[1] = uint32(uint64(v) >> 32)
	}
}

const ptrWords = int(ptrSize / 4)

// EDITSTREAM is EDITSTREAM: dwCookie (pointer-sized), dwError (4 bytes)
// and pfnCallback (pointer-sized) without padding.
type EDITSTREAM struct {
	w [(2*ptrSize + 4) / 4]uint32
}

func (es *EDITSTREAM) SetCookie(cookie uintptr) {
	setPtr(es.w[:], cookie)
}

func (es *EDITSTREAM) Error() uint32 {
	return es.w[ptrWords]
}

func (es *EDITSTREAM) SetCallback(callback uintptr) {
	setPtr(es.w[ptrWords+1:], callback)
}

// ENLINK is ENLINK: an NMHDR, then msg (4 bytes), wParam and lParam
// (pointer-sized) and chrg without padding. A pointer to it comes in the
// lParam of WM_NOTIFY.
type ENLINK struct {
	Nmhdr NMHDR
	w     [(4 + 2*ptrSize + 8) / 4]uint32
}

func (e *ENLINK) Msg() uint32 {
	return e.w[0]
}

func (e *ENLINK) WParam() uintptr {
	return getPtr(e.w[1:])
}

func (e *ENLINK) LParam() uintptr {
	return getPtr(e.w[1+ptrWords:])
}

func (e *ENLINK) Chrg() CHARRANGE {
	i := 1 + 2*ptrWords
	return CHARRANGE{CpMin: int32(e.w[i]), CpMax: int32(e.w[i+1])}
}
