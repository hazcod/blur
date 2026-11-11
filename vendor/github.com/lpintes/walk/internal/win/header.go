// Copyright 2012 The win Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package win

type HDITEM struct {
	Mask       uint32
	Cxy        int32
	PszText    *uint16
	Hbm        HBITMAP
	CchTextMax int32
	Fmt        int32
	LParam     uintptr
	IImage     int32
	IOrder     int32
	Type       uint32
	PvFilter   uintptr
}
