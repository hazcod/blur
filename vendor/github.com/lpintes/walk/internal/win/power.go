// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package win

import "unsafe"

// REASON_CONTEXT is hand-written because its Reason field is an anonymous
// union, which the generator rejects. Only the SimpleReasonString member
// of the union is declared; the padding makes the struct as large as the
// union's Detailed member (16 bytes on 386, 24 bytes on 64-bit).
type REASON_CONTEXT struct {
	Version            uint32
	Flags              uint32
	SimpleReasonString *uint16
	_                  [(ptrSize + 8) / ptrSize]uintptr
}

// Sizes from the Windows SDK (24 bytes on 386, 32 bytes on 64-bit); the
// build fails if the Go struct differs.
var (
	_ [unsafe.Sizeof(REASON_CONTEXT{}) - (8 + 2*ptrSize + 8)]byte
	_ [(8 + 2*ptrSize + 8) - unsafe.Sizeof(REASON_CONTEXT{})]byte
	_ [unsafe.Offsetof(REASON_CONTEXT{}.SimpleReasonString) - 8]byte
	_ [8 - unsafe.Offsetof(REASON_CONTEXT{}.SimpleReasonString)]byte
)
