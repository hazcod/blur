// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package uia

import (
	"strconv"

	"github.com/lpintes/walk/internal/win"
)

// Scope selects the elements FindFirst and FindAll look at, relative to the
// element they are called on.
type Scope int32

const (
	// ScopeElement is the element itself.
	ScopeElement Scope = win.TreeScope_Element
	// ScopeChildren are the direct children of the element.
	ScopeChildren Scope = win.TreeScope_Children
	// ScopeDescendants are all elements below the element.
	ScopeDescendants Scope = win.TreeScope_Descendants
	// ScopeSubtree is the element and all elements below it.
	ScopeSubtree Scope = win.TreeScope_Subtree
)

// ControlType is the kind of control an element is, such as a button or an
// edit field. Screen readers announce it, in the language of the system,
// as the LocalizedControlType of the element.
type ControlType int32

const (
	ControlTypeButton       ControlType = win.UIA_ButtonControlTypeId
	ControlTypeCalendar     ControlType = win.UIA_CalendarControlTypeId
	ControlTypeCheckBox     ControlType = win.UIA_CheckBoxControlTypeId
	ControlTypeComboBox     ControlType = win.UIA_ComboBoxControlTypeId
	ControlTypeEdit         ControlType = win.UIA_EditControlTypeId
	ControlTypeHyperlink    ControlType = win.UIA_HyperlinkControlTypeId
	ControlTypeImage        ControlType = win.UIA_ImageControlTypeId
	ControlTypeListItem     ControlType = win.UIA_ListItemControlTypeId
	ControlTypeList         ControlType = win.UIA_ListControlTypeId
	ControlTypeMenu         ControlType = win.UIA_MenuControlTypeId
	ControlTypeMenuBar      ControlType = win.UIA_MenuBarControlTypeId
	ControlTypeMenuItem     ControlType = win.UIA_MenuItemControlTypeId
	ControlTypeProgressBar  ControlType = win.UIA_ProgressBarControlTypeId
	ControlTypeRadioButton  ControlType = win.UIA_RadioButtonControlTypeId
	ControlTypeScrollBar    ControlType = win.UIA_ScrollBarControlTypeId
	ControlTypeSlider       ControlType = win.UIA_SliderControlTypeId
	ControlTypeSpinner      ControlType = win.UIA_SpinnerControlTypeId
	ControlTypeStatusBar    ControlType = win.UIA_StatusBarControlTypeId
	ControlTypeTab          ControlType = win.UIA_TabControlTypeId
	ControlTypeTabItem      ControlType = win.UIA_TabItemControlTypeId
	ControlTypeText         ControlType = win.UIA_TextControlTypeId
	ControlTypeToolBar      ControlType = win.UIA_ToolBarControlTypeId
	ControlTypeToolTip      ControlType = win.UIA_ToolTipControlTypeId
	ControlTypeTree         ControlType = win.UIA_TreeControlTypeId
	ControlTypeTreeItem     ControlType = win.UIA_TreeItemControlTypeId
	ControlTypeCustom       ControlType = win.UIA_CustomControlTypeId
	ControlTypeGroup        ControlType = win.UIA_GroupControlTypeId
	ControlTypeThumb        ControlType = win.UIA_ThumbControlTypeId
	ControlTypeDataGrid     ControlType = win.UIA_DataGridControlTypeId
	ControlTypeDataItem     ControlType = win.UIA_DataItemControlTypeId
	ControlTypeDocument     ControlType = win.UIA_DocumentControlTypeId
	ControlTypeSplitButton  ControlType = win.UIA_SplitButtonControlTypeId
	ControlTypeWindow       ControlType = win.UIA_WindowControlTypeId
	ControlTypePane         ControlType = win.UIA_PaneControlTypeId
	ControlTypeHeader       ControlType = win.UIA_HeaderControlTypeId
	ControlTypeHeaderItem   ControlType = win.UIA_HeaderItemControlTypeId
	ControlTypeTable        ControlType = win.UIA_TableControlTypeId
	ControlTypeTitleBar     ControlType = win.UIA_TitleBarControlTypeId
	ControlTypeSeparator    ControlType = win.UIA_SeparatorControlTypeId
	ControlTypeSemanticZoom ControlType = win.UIA_SemanticZoomControlTypeId
	ControlTypeAppBar       ControlType = win.UIA_AppBarControlTypeId
)

var controlTypeNames = []string{
	"Button", "Calendar", "CheckBox", "ComboBox", "Edit", "Hyperlink",
	"Image", "ListItem", "List", "Menu", "MenuBar", "MenuItem",
	"ProgressBar", "RadioButton", "ScrollBar", "Slider", "Spinner",
	"StatusBar", "Tab", "TabItem", "Text", "ToolBar", "ToolTip", "Tree",
	"TreeItem", "Custom", "Group", "Thumb", "DataGrid", "DataItem",
	"Document", "SplitButton", "Window", "Pane", "Header", "HeaderItem",
	"Table", "TitleBar", "Separator", "SemanticZoom", "AppBar",
}

// String returns the English name of the control type, such as "Button",
// or "ControlType(N)" for an unknown value.
func (t ControlType) String() string {
	if i := int(t - ControlTypeButton); i >= 0 && i < len(controlTypeNames) {
		return controlTypeNames[i]
	}
	return "ControlType(" + strconv.Itoa(int(t)) + ")"
}

// PropertyID identifies an element property in a Condition, a
// CacheRequest or PropertyValue.
type PropertyID int32

// The properties that have a getter method on Element, with the Go type of
// the value Property takes for them.
const (
	PropertyName                 PropertyID = win.UIA_NamePropertyId                 // string
	PropertyControlType          PropertyID = win.UIA_ControlTypePropertyId          // ControlType
	PropertyLocalizedControlType PropertyID = win.UIA_LocalizedControlTypePropertyId // string
	PropertyAutomationID         PropertyID = win.UIA_AutomationIdPropertyId         // string
	PropertyClassName            PropertyID = win.UIA_ClassNamePropertyId            // string
	PropertyHelpText             PropertyID = win.UIA_HelpTextPropertyId             // string
	PropertyAccessKey            PropertyID = win.UIA_AccessKeyPropertyId            // string
	PropertyAcceleratorKey       PropertyID = win.UIA_AcceleratorKeyPropertyId       // string
	PropertyItemStatus           PropertyID = win.UIA_ItemStatusPropertyId           // string
	PropertyItemType             PropertyID = win.UIA_ItemTypePropertyId             // string
	PropertyFrameworkID          PropertyID = win.UIA_FrameworkIdPropertyId          // string
	PropertyProcessID            PropertyID = win.UIA_ProcessIdPropertyId            // int
	PropertyNativeWindowHandle   PropertyID = win.UIA_NativeWindowHandlePropertyId   // int
	PropertyIsEnabled            PropertyID = win.UIA_IsEnabledPropertyId            // bool
	PropertyHasKeyboardFocus     PropertyID = win.UIA_HasKeyboardFocusPropertyId     // bool
	PropertyIsKeyboardFocusable  PropertyID = win.UIA_IsKeyboardFocusablePropertyId  // bool
	PropertyIsOffscreen          PropertyID = win.UIA_IsOffscreenPropertyId          // bool
	PropertyIsPassword           PropertyID = win.UIA_IsPasswordPropertyId           // bool
	PropertyIsControlElement     PropertyID = win.UIA_IsControlElementPropertyId     // bool
	PropertyIsContentElement     PropertyID = win.UIA_IsContentElementPropertyId     // bool
	PropertyIsRequiredForForm    PropertyID = win.UIA_IsRequiredForFormPropertyId    // bool
	PropertyIsDialog             PropertyID = win.UIA_IsDialogPropertyId             // bool
)

// Properties without a getter method on Element, for PropertyValue,
// CachedPropertyValue, CacheRequest and conditions.
const (
	// PropertyLiveSetting is the live setting of a live region: 0 off,
	// 1 polite, 2 assertive.
	PropertyLiveSetting PropertyID = win.UIA_LiveSettingPropertyId // int
)
