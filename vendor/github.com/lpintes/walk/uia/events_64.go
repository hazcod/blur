// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows && (amd64 || arm64)

package uia

import (
	"github.com/lpintes/walk/internal/com"
	"github.com/lpintes/walk/internal/win"
)

// handlePropertyChangedEvent is the HandlePropertyChangedEvent method. On
// amd64 and arm64 the 24-byte VARIANT newValue is passed as a pointer to a
// copy.
func handlePropertyChangedEvent(this *com.This, sender *win.IUIAutomationElement, propertyID int32, v *win.VARIANT) uintptr {
	return handlePropertyChanged(this, sender, propertyID, v)
}
