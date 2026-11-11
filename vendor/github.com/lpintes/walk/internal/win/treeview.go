// Copyright 2010 The win Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package win

const (
	TVI_ROOT  = ^HTREEITEM(0xffff)
	TVI_FIRST = ^HTREEITEM(0xfffe)
)

type HTREEITEM HANDLE

/*type TVITEMEX struct {
	mask           UINT
	hItem          HTREEITEM
	state          UINT
	stateMask      UINT
	pszText        LPWSTR
	cchTextMax     int
	iImage         int
	iSelectedImage int
	cChildren      int
	lParam         LPARAM
	iIntegral      int
	uStateEx       UINT
	hwnd           HWND
	iExpandedImage int
}*/

type TVINSERTSTRUCT struct {
	HParent      HTREEITEM
	HInsertAfter HTREEITEM
	Item         TVITEM
	//	itemex       TVITEMEX
}

type NMTVKEYDOWN struct {
	Hdr   NMHDR
	WVKey uint16
	Flags uint32
}
