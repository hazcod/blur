// Copyright 2010 The win Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package win

import ()

const (
	HKEY_CLASSES_ROOT  HKEY = 0x80000000
	HKEY_CURRENT_USER  HKEY = 0x80000001
	HKEY_LOCAL_MACHINE HKEY = 0x80000002
)

type (
	ACCESS_MASK uint32
	HKEY        HANDLE
	REGSAM      ACCESS_MASK
)
