// Copyright 2010 The win Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package win

// ToolBar notifications
const (
	TBN_FIRST    = -700
	TBN_DROPDOWN = TBN_FIRST - 10
)

type TBBUTTON struct {
	IBitmap   int32
	IdCommand int32
	FsState   byte
	FsStyle   byte
	//#ifdef _WIN64
	//    BYTE bReserved[6]          // padding for alignment
	//#elif defined(_WIN32)
	BReserved [2]byte // padding for alignment
	//#endif
	DwData  uintptr
	IString uintptr
}
