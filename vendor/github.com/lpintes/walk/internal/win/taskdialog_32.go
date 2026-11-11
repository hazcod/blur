// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build (windows && 386) || (windows && arm)

package win

// The build fails if the packed sizes differ from sizeof in the SDK headers
// on 32-bit Windows: TASKDIALOGCONFIG 96 bytes, TASKDIALOG_BUTTON 8.
var (
	_ [sizeofTASKDIALOGCONFIG - 96]byte
	_ [96 - sizeofTASKDIALOGCONFIG]byte
	_ [sizeofTASKDIALOG_BUTTON - 8]byte
	_ [8 - sizeofTASKDIALOG_BUTTON]byte
)
