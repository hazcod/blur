// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build (windows && amd64) || (windows && arm64)

package win

// The build fails if the packed sizes differ from sizeof in the SDK headers
// on 64-bit Windows: TASKDIALOGCONFIG 160 bytes, TASKDIALOG_BUTTON 12
// (#pragma pack(1); naturally aligned they would be 176 and 16).
var (
	_ [sizeofTASKDIALOGCONFIG - 160]byte
	_ [160 - sizeofTASKDIALOGCONFIG]byte
	_ [sizeofTASKDIALOG_BUTTON - 12]byte
	_ [12 - sizeofTASKDIALOG_BUTTON]byte
)
