// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows && 386

package uia

import (
	"unsafe"

	"github.com/lpintes/walk/internal/com"
	"github.com/lpintes/walk/internal/win"
)

// handlePropertyChangedEvent is the HandlePropertyChangedEvent method. On
// 386 the 16-byte VARIANT newValue is passed by value as four stack words.
func handlePropertyChangedEvent(this *com.This, sender *win.IUIAutomationElement, propertyID int32, v0, v1, v2, v3 uintptr) uintptr {
	v := [4]uintptr{v0, v1, v2, v3}
	return handlePropertyChanged(this, sender, propertyID, (*win.VARIANT)(unsafe.Pointer(&v)))
}
