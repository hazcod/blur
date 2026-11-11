// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package uia

import (
	"image"
	"runtime"

	"golang.org/x/sys/windows"

	"github.com/lpintes/walk/internal/win"
)

// Element is an object in the UI Automation tree, such as a window, a
// button or a list item. Its properties are read from the program that
// owns it at each call, so they are always current. The methods of a nil
// *Element return an error.
type Element struct {
	a *Automation
	p *win.IUIAutomationElement // used only on the thread of a
}

// Release frees the element. Its methods return ErrReleased afterwards.
// Release returns nil, also when called again; after Close of the
// Automation it returns ErrClosed. Release of a nil Element returns nil.
func (e *Element) Release() error {
	if e == nil {
		return nil
	}
	err := e.a.do(func() error {
		if e.p != nil {
			e.p.Release()
			e.p = nil
		}
		return nil
	})
	if err == nil {
		runtime.SetFinalizer(e, nil)
	}
	return err
}

// call runs f with the COM object of e on the thread of the Automation.
// For a nil e it returns an error instead, so that the methods of a nil
// Element, such as one a search did not find, fail without a panic.
func (e *Element) call(f func(p *win.IUIAutomationElement) error) error {
	if e == nil {
		return errNilElement
	}
	return e.a.do(func() error {
		if e.p == nil {
			return ErrReleased
		}
		return f(e.p)
	})
}

func (e *Element) stringProperty(op string, get func(*win.IUIAutomationElement, **uint16) win.HRESULT) (string, error) {
	var s string
	err := e.call(func(p *win.IUIAutomationElement) error {
		var bstr *uint16
		if hr := get(p, &bstr); win.FAILED(hr) {
			return hresultError(op, hr)
		}
		defer win.SysFreeString(bstr)
		s = windows.UTF16PtrToString(bstr)
		return nil
	})
	return s, err
}

func (e *Element) boolProperty(op string, get func(*win.IUIAutomationElement, *win.BOOL) win.HRESULT) (bool, error) {
	var b win.BOOL
	err := e.call(func(p *win.IUIAutomationElement) error {
		if hr := get(p, &b); win.FAILED(hr) {
			return hresultError(op, hr)
		}
		return nil
	})
	return b != win.FALSE, err
}

func (e *Element) intProperty(op string, get func(*win.IUIAutomationElement, *int32) win.HRESULT) (int32, error) {
	var n int32
	err := e.call(func(p *win.IUIAutomationElement) error {
		if hr := get(p, &n); win.FAILED(hr) {
			return hresultError(op, hr)
		}
		return nil
	})
	return n, err
}

// Name returns the name of the element, which screen readers read first:
// the text of a button, the label of an edit field, the title of a
// window.
func (e *Element) Name() (string, error) {
	return e.stringProperty("Name", (*win.IUIAutomationElement).Get_CurrentName)
}

// ControlType returns the kind of control the element is.
func (e *Element) ControlType() (ControlType, error) {
	n, err := e.intProperty("ControlType", (*win.IUIAutomationElement).Get_CurrentControlType)
	return ControlType(n), err
}

// LocalizedControlType returns the control type as screen readers announce
// it, in the language of the system, for example "button".
func (e *Element) LocalizedControlType() (string, error) {
	return e.stringProperty("LocalizedControlType", (*win.IUIAutomationElement).Get_CurrentLocalizedControlType)
}

// AutomationID returns the identifier of the element among its siblings,
// meant for tests and tools. For a standard Win32 control it is the
// control ID.
func (e *Element) AutomationID() (string, error) {
	return e.stringProperty("AutomationId", (*win.IUIAutomationElement).Get_CurrentAutomationId)
}

// ClassName returns the class name of the element, for a window its window
// class.
func (e *Element) ClassName() (string, error) {
	return e.stringProperty("ClassName", (*win.IUIAutomationElement).Get_CurrentClassName)
}

// HelpText returns the help text of the element, often its tooltip.
func (e *Element) HelpText() (string, error) {
	return e.stringProperty("HelpText", (*win.IUIAutomationElement).Get_CurrentHelpText)
}

// AccessKey returns the access key of the element, for example "Alt+F".
func (e *Element) AccessKey() (string, error) {
	return e.stringProperty("AccessKey", (*win.IUIAutomationElement).Get_CurrentAccessKey)
}

// AcceleratorKey returns the shortcut key of the element, for example
// "Ctrl+O".
func (e *Element) AcceleratorKey() (string, error) {
	return e.stringProperty("AcceleratorKey", (*win.IUIAutomationElement).Get_CurrentAcceleratorKey)
}

// ItemStatus returns the status of an item as its program describes it,
// for example "busy".
func (e *Element) ItemStatus() (string, error) {
	return e.stringProperty("ItemStatus", (*win.IUIAutomationElement).Get_CurrentItemStatus)
}

// ItemType returns what kind of object an item stands for, as its program
// describes it, for example "text document".
func (e *Element) ItemType() (string, error) {
	return e.stringProperty("ItemType", (*win.IUIAutomationElement).Get_CurrentItemType)
}

// FrameworkID returns the user interface framework of the element, for
// example "Win32", "WPF" or "Chrome".
func (e *Element) FrameworkID() (string, error) {
	return e.stringProperty("FrameworkId", (*win.IUIAutomationElement).Get_CurrentFrameworkId)
}

// ProcessID returns the ID of the process that owns the element.
func (e *Element) ProcessID() (int, error) {
	n, err := e.intProperty("ProcessId", (*win.IUIAutomationElement).Get_CurrentProcessId)
	return int(n), err
}

// NativeWindowHandle returns the window handle of the element, or 0 if the
// element is not a window of its own, like a list item.
func (e *Element) NativeWindowHandle() (uintptr, error) {
	var hwnd win.HWND
	err := e.call(func(p *win.IUIAutomationElement) error {
		if hr := p.Get_CurrentNativeWindowHandle(&hwnd); win.FAILED(hr) {
			return hresultError("NativeWindowHandle", hr)
		}
		return nil
	})
	return uintptr(hwnd), err
}

// BoundingRectangle returns the rectangle of the element on the screen, in
// physical pixels. It is empty for an element that is not visible.
func (e *Element) BoundingRectangle() (image.Rectangle, error) {
	var r win.RECT
	err := e.call(func(p *win.IUIAutomationElement) error {
		if hr := p.Get_CurrentBoundingRectangle(&r); win.FAILED(hr) {
			return hresultError("BoundingRectangle", hr)
		}
		return nil
	})
	return image.Rect(int(r.Left), int(r.Top), int(r.Right), int(r.Bottom)), err
}

// IsEnabled reports whether the element can be used.
func (e *Element) IsEnabled() (bool, error) {
	return e.boolProperty("IsEnabled", (*win.IUIAutomationElement).Get_CurrentIsEnabled)
}

// HasKeyboardFocus reports whether the element has the keyboard focus.
func (e *Element) HasKeyboardFocus() (bool, error) {
	return e.boolProperty("HasKeyboardFocus", (*win.IUIAutomationElement).Get_CurrentHasKeyboardFocus)
}

// IsKeyboardFocusable reports whether the element can get the keyboard
// focus.
func (e *Element) IsKeyboardFocusable() (bool, error) {
	return e.boolProperty("IsKeyboardFocusable", (*win.IUIAutomationElement).Get_CurrentIsKeyboardFocusable)
}

// IsOffscreen reports whether the element is not visible on the screen,
// for example because it is hidden or scrolled out of view.
func (e *Element) IsOffscreen() (bool, error) {
	return e.boolProperty("IsOffscreen", (*win.IUIAutomationElement).Get_CurrentIsOffscreen)
}

// IsPassword reports whether the element is a password field.
func (e *Element) IsPassword() (bool, error) {
	return e.boolProperty("IsPassword", (*win.IUIAutomationElement).Get_CurrentIsPassword)
}

// IsControlElement reports whether the element is a control a user
// interacts with, as opposed to a part of a control's structure. Elements
// of the control view are control elements.
func (e *Element) IsControlElement() (bool, error) {
	return e.boolProperty("IsControlElement", (*win.IUIAutomationElement).Get_CurrentIsControlElement)
}

// IsContentElement reports whether the element holds information for the
// user, as opposed to a decoration. Elements of the content view are
// content elements.
func (e *Element) IsContentElement() (bool, error) {
	return e.boolProperty("IsContentElement", (*win.IUIAutomationElement).Get_CurrentIsContentElement)
}

// IsRequiredForForm reports whether the element must be filled in.
func (e *Element) IsRequiredForForm() (bool, error) {
	return e.boolProperty("IsRequiredForForm", (*win.IUIAutomationElement).Get_CurrentIsRequiredForForm)
}

// IsDialog reports whether the element is a dialog window, as UI Automation
// reports for system dialogs (the #32770 window class) and for windows
// annotated as dialogs, such as walk dialogs. The property exists since
// Windows 10 version 1809; before it IsDialog returns false.
func (e *Element) IsDialog() (bool, error) {
	var isDialog bool
	err := e.call(func(p *win.IUIAutomationElement) error {
		var v win.VARIANT
		if hr := p.GetCurrentPropertyValue(win.UIA_IsDialogPropertyId, &v); win.FAILED(hr) {
			if uint32(hr) == win.E_INVALIDARG {
				// Before Windows 10 1809 UI Automation does not know the property.
				return nil
			}
			return hresultError("IsDialog", hr)
		}
		defer win.VariantClear(&v)
		if b, err := v.Bool(); err == nil {
			isDialog = b != win.VARIANT_FALSE
		}
		return nil
	})
	return isDialog, err
}

// LabeledBy returns the element that labels this one, usually a text next
// to an edit field, or nil if there is none.
func (e *Element) LabeledBy() (*Element, error) {
	var label *Element
	err := e.call(func(p *win.IUIAutomationElement) error {
		var l *win.IUIAutomationElement
		if hr := p.Get_CurrentLabeledBy(&l); win.FAILED(hr) {
			return hresultError("LabeledBy", hr)
		}
		label = e.a.newElement(l)
		return nil
	})
	return label, err
}

// SetFocus moves the keyboard focus to the element. Windows may bring its
// window to the foreground.
func (e *Element) SetFocus() error {
	return e.call(func(p *win.IUIAutomationElement) error {
		if hr := p.SetFocus(); win.FAILED(hr) {
			return hresultError("SetFocus", hr)
		}
		return nil
	})
}

// Equal reports whether e and other are the same user interface object.
// Two Element values returned by different calls can be equal. Both must
// come from the same Automation.
func (e *Element) Equal(other *Element) (bool, error) {
	if e == nil {
		return false, errNilElement
	}
	if other == nil {
		return false, nil
	}
	if other.a != e.a {
		return false, errOtherAutomation
	}
	var same win.BOOL
	err := e.call(func(p *win.IUIAutomationElement) error {
		if other.p == nil {
			return ErrReleased
		}
		if hr := e.a.uia.CompareElements(p, other.p, &same); win.FAILED(hr) {
			return hresultError("CompareElements", hr)
		}
		return nil
	})
	return same != win.FALSE, err
}

// FindFirst returns the first element in scope that matches cond, or nil
// if there is none. With ScopeDescendants or ScopeSubtree it may take a
// long time on the root element.
func (e *Element) FindFirst(scope Scope, cond Condition) (*Element, error) {
	var found *Element
	err := e.call(func(p *win.IUIAutomationElement) error {
		c, err := createCondition(e.a.uia, cond)
		if err != nil {
			return err
		}
		defer c.Release()
		var f *win.IUIAutomationElement
		if hr := p.FindFirst(int32(scope), c, &f); win.FAILED(hr) {
			return hresultError("FindFirst", hr)
		}
		found = e.a.newElement(f)
		return nil
	})
	return found, err
}

// FindAll returns all elements in scope that match cond, in tree order.
func (e *Element) FindAll(scope Scope, cond Condition) ([]*Element, error) {
	var found []*Element
	err := e.call(func(p *win.IUIAutomationElement) error {
		c, err := createCondition(e.a.uia, cond)
		if err != nil {
			return err
		}
		defer c.Release()
		var array *win.IUIAutomationElementArray
		if hr := p.FindAll(int32(scope), c, &array); win.FAILED(hr) {
			return hresultError("FindAll", hr)
		}
		found, err = e.a.elements("FindAll", array)
		return err
	})
	return found, err
}

// elements converts and releases array, which may be nil. It runs on the
// thread of the Automation.
func (a *Automation) elements(op string, array *win.IUIAutomationElementArray) ([]*Element, error) {
	if array == nil {
		return nil, nil
	}
	defer array.Release()
	var n int32
	if hr := array.Get_Length(&n); win.FAILED(hr) {
		return nil, hresultError(op, hr)
	}
	elements := make([]*Element, 0, n)
	for i := range n {
		var p *win.IUIAutomationElement
		if hr := array.GetElement(i, &p); win.FAILED(hr) {
			for _, e := range elements {
				e.p.Release()
				e.p = nil
				runtime.SetFinalizer(e, nil)
			}
			return nil, hresultError(op, hr)
		}
		elements = append(elements, a.newElement(p))
	}
	return elements, nil
}
