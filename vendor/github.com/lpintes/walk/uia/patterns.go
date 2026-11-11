// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package uia

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/lpintes/walk/internal/win"
)

// ErrPatternNotSupported is wrapped by the errors of the methods that need
// a control pattern the element does not support, for example Invoke on a
// text.
var ErrPatternNotSupported = errors.New("uia: control pattern not supported")

// comPattern is the pointer type of a generated pattern interface.
type comPattern[T any] interface {
	*T
	Release() uint32
}

// withPattern gets the control pattern id of e as the interface iid and
// runs f with it on the thread of the Automation.
func withPattern[T any, P comPattern[T]](e *Element, op string, id int32, iid *win.IID, f func(p P) error) error {
	return e.call(func(el *win.IUIAutomationElement) error {
		var ptr unsafe.Pointer
		if hr := el.GetCurrentPatternAs(id, (*syscall.GUID)(iid), &ptr); win.FAILED(hr) {
			return hresultError(op, hr)
		}
		if ptr == nil {
			return fmt.Errorf("uia: %s: %w", op, ErrPatternNotSupported)
		}
		p := P((*T)(ptr))
		defer p.Release()
		return f(p)
	})
}

// check turns a failed HRESULT into an error.
func check(op string, hr win.HRESULT) error {
	if win.FAILED(hr) {
		return hresultError(op, hr)
	}
	return nil
}

// bstr converts and frees a BSTR returned by a successful call.
func bstr(b *uint16) string {
	defer win.SysFreeString(b)
	return windows.UTF16PtrToString(b)
}

// Invoke pattern

// Invoke does the single action of the element, like a click on a button,
// a menu item or a link (the Invoke pattern).
func (e *Element) Invoke() error {
	return withPattern(e, "Invoke", win.UIA_InvokePatternId, &win.IID_IUIAutomationInvokePattern, func(p *win.IUIAutomationInvokePattern) error {
		return check("Invoke", p.Invoke())
	})
}

// Value pattern

// Value returns the text value of the element, for example the text of an
// edit field or the selected text of a combo box (the Value pattern).
func (e *Element) Value() (string, error) {
	var value string
	err := withPattern(e, "Value", win.UIA_ValuePatternId, &win.IID_IUIAutomationValuePattern, func(p *win.IUIAutomationValuePattern) error {
		var b *uint16
		if err := check("Value", p.Get_CurrentValue(&b)); err != nil {
			return err
		}
		value = bstr(b)
		return nil
	})
	return value, err
}

// SetValue sets the text value of the element (the Value pattern). It
// fails for a read-only value.
func (e *Element) SetValue(value string) error {
	value16, err := windows.UTF16PtrFromString(value)
	if err != nil {
		return fmt.Errorf("uia: SetValue: %w", err)
	}
	return withPattern(e, "SetValue", win.UIA_ValuePatternId, &win.IID_IUIAutomationValuePattern, func(p *win.IUIAutomationValuePattern) error {
		return check("SetValue", p.SetValue(value16))
	})
}

// IsValueReadOnly reports whether the text value of the element cannot be
// changed (the Value pattern).
func (e *Element) IsValueReadOnly() (bool, error) {
	var readOnly win.BOOL
	err := withPattern(e, "IsValueReadOnly", win.UIA_ValuePatternId, &win.IID_IUIAutomationValuePattern, func(p *win.IUIAutomationValuePattern) error {
		return check("IsValueReadOnly", p.Get_CurrentIsReadOnly(&readOnly))
	})
	return readOnly != win.FALSE, err
}

// RangeValue pattern

// RangeValue is the numeric value of a slider, progress bar, spinner or
// scroll bar.
type RangeValue struct {
	Value       float64
	Minimum     float64
	Maximum     float64
	SmallChange float64 // the change of an arrow key
	LargeChange float64 // the change of Page Up or Page Down
	IsReadOnly  bool
}

// RangeValue returns the numeric value of the element and its range (the
// RangeValue pattern).
func (e *Element) RangeValue() (RangeValue, error) {
	var rv RangeValue
	err := withPattern(e, "RangeValue", win.UIA_RangeValuePatternId, &win.IID_IUIAutomationRangeValuePattern, func(p *win.IUIAutomationRangeValuePattern) error {
		var readOnly win.BOOL
		for _, hr := range []win.HRESULT{
			p.Get_CurrentValue(&rv.Value),
			p.Get_CurrentMinimum(&rv.Minimum),
			p.Get_CurrentMaximum(&rv.Maximum),
			p.Get_CurrentSmallChange(&rv.SmallChange),
			p.Get_CurrentLargeChange(&rv.LargeChange),
			p.Get_CurrentIsReadOnly(&readOnly),
		} {
			if err := check("RangeValue", hr); err != nil {
				return err
			}
		}
		rv.IsReadOnly = readOnly != win.FALSE
		return nil
	})
	return rv, err
}

// SetRangeValue sets the numeric value of the element (the RangeValue
// pattern). The value must be between the minimum and the maximum. On
// arm64 it is not supported yet and returns an error.
func (e *Element) SetRangeValue(value float64) error {
	return withPattern(e, "SetRangeValue", win.UIA_RangeValuePatternId, &win.IID_IUIAutomationRangeValuePattern, func(p *win.IUIAutomationRangeValuePattern) error {
		return check("SetRangeValue", p.SetValue(value))
	})
}

// Toggle pattern

// ToggleState is the state of a check box or toggle button.
type ToggleState int32

const (
	ToggleOff           ToggleState = win.ToggleState_Off
	ToggleOn            ToggleState = win.ToggleState_On
	ToggleIndeterminate ToggleState = win.ToggleState_Indeterminate
)

// String returns "off", "on" or "indeterminate".
func (s ToggleState) String() string {
	switch s {
	case ToggleOff:
		return "off"
	case ToggleOn:
		return "on"
	case ToggleIndeterminate:
		return "indeterminate"
	}
	return fmt.Sprintf("ToggleState(%d)", int32(s))
}

// ToggleState returns the state of a check box or toggle button (the
// Toggle pattern).
func (e *Element) ToggleState() (ToggleState, error) {
	var state int32
	err := withPattern(e, "ToggleState", win.UIA_TogglePatternId, &win.IID_IUIAutomationTogglePattern, func(p *win.IUIAutomationTogglePattern) error {
		return check("ToggleState", p.Get_CurrentToggleState(&state))
	})
	return ToggleState(state), err
}

// Toggle moves a check box or toggle button to its next state, like a
// press of the space bar (the Toggle pattern).
func (e *Element) Toggle() error {
	return withPattern(e, "Toggle", win.UIA_TogglePatternId, &win.IID_IUIAutomationTogglePattern, func(p *win.IUIAutomationTogglePattern) error {
		return check("Toggle", p.Toggle())
	})
}

// ExpandCollapse pattern

// ExpandCollapseState is the state of a tree item, combo box or menu that
// can show and hide its children.
type ExpandCollapseState int32

const (
	Collapsed         ExpandCollapseState = win.ExpandCollapseState_Collapsed
	Expanded          ExpandCollapseState = win.ExpandCollapseState_Expanded
	PartiallyExpanded ExpandCollapseState = win.ExpandCollapseState_PartiallyExpanded
	// LeafNode is an element without children to show.
	LeafNode ExpandCollapseState = win.ExpandCollapseState_LeafNode
)

// String returns "collapsed", "expanded", "partially expanded" or "leaf".
func (s ExpandCollapseState) String() string {
	switch s {
	case Collapsed:
		return "collapsed"
	case Expanded:
		return "expanded"
	case PartiallyExpanded:
		return "partially expanded"
	case LeafNode:
		return "leaf"
	}
	return fmt.Sprintf("ExpandCollapseState(%d)", int32(s))
}

// ExpandCollapseState returns whether the element shows its children (the
// ExpandCollapse pattern).
func (e *Element) ExpandCollapseState() (ExpandCollapseState, error) {
	var state int32
	err := withPattern(e, "ExpandCollapseState", win.UIA_ExpandCollapsePatternId, &win.IID_IUIAutomationExpandCollapsePattern, func(p *win.IUIAutomationExpandCollapsePattern) error {
		return check("ExpandCollapseState", p.Get_CurrentExpandCollapseState(&state))
	})
	return ExpandCollapseState(state), err
}

// Expand shows the children of the element, for example opens a combo box
// or expands a tree item (the ExpandCollapse pattern).
func (e *Element) Expand() error {
	return withPattern(e, "Expand", win.UIA_ExpandCollapsePatternId, &win.IID_IUIAutomationExpandCollapsePattern, func(p *win.IUIAutomationExpandCollapsePattern) error {
		return check("Expand", p.Expand())
	})
}

// Collapse hides the children of the element (the ExpandCollapse
// pattern).
func (e *Element) Collapse() error {
	return withPattern(e, "Collapse", win.UIA_ExpandCollapsePatternId, &win.IID_IUIAutomationExpandCollapsePattern, func(p *win.IUIAutomationExpandCollapsePattern) error {
		return check("Collapse", p.Collapse())
	})
}

// SelectionItem pattern

// Select selects the element, a list item, tab or radio button, and
// deselects the others (the SelectionItem pattern).
func (e *Element) Select() error {
	return withPattern(e, "Select", win.UIA_SelectionItemPatternId, &win.IID_IUIAutomationSelectionItemPattern, func(p *win.IUIAutomationSelectionItemPattern) error {
		return check("Select", p.Select())
	})
}

// AddToSelection selects the element and keeps the other selected items,
// in a container that allows several (the SelectionItem pattern).
func (e *Element) AddToSelection() error {
	return withPattern(e, "AddToSelection", win.UIA_SelectionItemPatternId, &win.IID_IUIAutomationSelectionItemPattern, func(p *win.IUIAutomationSelectionItemPattern) error {
		return check("AddToSelection", p.AddToSelection())
	})
}

// RemoveFromSelection deselects the element (the SelectionItem pattern).
func (e *Element) RemoveFromSelection() error {
	return withPattern(e, "RemoveFromSelection", win.UIA_SelectionItemPatternId, &win.IID_IUIAutomationSelectionItemPattern, func(p *win.IUIAutomationSelectionItemPattern) error {
		return check("RemoveFromSelection", p.RemoveFromSelection())
	})
}

// IsSelected reports whether the element is selected (the SelectionItem
// pattern).
func (e *Element) IsSelected() (bool, error) {
	var selected win.BOOL
	err := withPattern(e, "IsSelected", win.UIA_SelectionItemPatternId, &win.IID_IUIAutomationSelectionItemPattern, func(p *win.IUIAutomationSelectionItemPattern) error {
		return check("IsSelected", p.Get_CurrentIsSelected(&selected))
	})
	return selected != win.FALSE, err
}

// SelectionContainer returns the element whose selection the element
// belongs to, for example the list of a list item (the SelectionItem
// pattern).
func (e *Element) SelectionContainer() (*Element, error) {
	var container *Element
	err := withPattern(e, "SelectionContainer", win.UIA_SelectionItemPatternId, &win.IID_IUIAutomationSelectionItemPattern, func(p *win.IUIAutomationSelectionItemPattern) error {
		var c *win.IUIAutomationElement
		if err := check("SelectionContainer", p.Get_CurrentSelectionContainer(&c)); err != nil {
			return err
		}
		container = e.a.newElement(c)
		return nil
	})
	return container, err
}

// Selection pattern

// Selection returns the selected items of a list, tab control or other
// container (the Selection pattern).
func (e *Element) Selection() ([]*Element, error) {
	var selection []*Element
	err := withPattern(e, "Selection", win.UIA_SelectionPatternId, &win.IID_IUIAutomationSelectionPattern, func(p *win.IUIAutomationSelectionPattern) error {
		var array *win.IUIAutomationElementArray
		if err := check("Selection", p.GetCurrentSelection(&array)); err != nil {
			return err
		}
		var err error
		selection, err = e.a.elements("Selection", array)
		return err
	})
	return selection, err
}

// CanSelectMultiple reports whether several items of the container can be
// selected at once (the Selection pattern).
func (e *Element) CanSelectMultiple() (bool, error) {
	var multiple win.BOOL
	err := withPattern(e, "CanSelectMultiple", win.UIA_SelectionPatternId, &win.IID_IUIAutomationSelectionPattern, func(p *win.IUIAutomationSelectionPattern) error {
		return check("CanSelectMultiple", p.Get_CurrentCanSelectMultiple(&multiple))
	})
	return multiple != win.FALSE, err
}

// Text pattern

// Text returns the whole text of a document or edit field (the Text
// pattern). Unlike Value it also works for multiline and rich text.
func (e *Element) Text() (string, error) {
	var text string
	err := withPattern(e, "Text", win.UIA_TextPatternId, &win.IID_IUIAutomationTextPattern, func(p *win.IUIAutomationTextPattern) error {
		var r *win.IUIAutomationTextRange
		if err := check("Text", p.Get_DocumentRange(&r)); err != nil {
			return err
		}
		defer r.Release()
		var b *uint16
		if err := check("Text", r.GetText(-1, &b)); err != nil {
			return err
		}
		text = bstr(b)
		return nil
	})
	return text, err
}

// SelectedText returns the selected text of a document or edit field, one
// string per selected range (the Text pattern). Where nothing is selected,
// the controls usually return one empty range at the caret.
func (e *Element) SelectedText() ([]string, error) {
	var texts []string
	err := withPattern(e, "SelectedText", win.UIA_TextPatternId, &win.IID_IUIAutomationTextPattern, func(p *win.IUIAutomationTextPattern) error {
		var ranges *win.IUIAutomationTextRangeArray
		if err := check("SelectedText", p.GetSelection(&ranges)); err != nil {
			return err
		}
		if ranges == nil {
			return nil
		}
		defer ranges.Release()
		var n int32
		if err := check("SelectedText", ranges.Get_Length(&n)); err != nil {
			return err
		}
		for i := range n {
			var r *win.IUIAutomationTextRange
			if err := check("SelectedText", ranges.GetElement(i, &r)); err != nil {
				return err
			}
			var b *uint16
			hr := r.GetText(-1, &b)
			r.Release()
			if err := check("SelectedText", hr); err != nil {
				return err
			}
			texts = append(texts, bstr(b))
		}
		return nil
	})
	return texts, err
}

// Window pattern

// WindowVisualState is the state of a top-level window.
type WindowVisualState int32

const (
	WindowNormal    WindowVisualState = win.WindowVisualState_Normal
	WindowMaximized WindowVisualState = win.WindowVisualState_Maximized
	WindowMinimized WindowVisualState = win.WindowVisualState_Minimized
)

// String returns "normal", "maximized" or "minimized".
func (s WindowVisualState) String() string {
	switch s {
	case WindowNormal:
		return "normal"
	case WindowMaximized:
		return "maximized"
	case WindowMinimized:
		return "minimized"
	}
	return fmt.Sprintf("WindowVisualState(%d)", int32(s))
}

// CloseWindow closes the window, like its close button (the Window
// pattern). The program may ask before closing.
func (e *Element) CloseWindow() error {
	return withPattern(e, "CloseWindow", win.UIA_WindowPatternId, &win.IID_IUIAutomationWindowPattern, func(p *win.IUIAutomationWindowPattern) error {
		return check("CloseWindow", p.Close())
	})
}

// WindowVisualState returns whether the window is normal, maximized or
// minimized (the Window pattern).
func (e *Element) WindowVisualState() (WindowVisualState, error) {
	var state int32
	err := withPattern(e, "WindowVisualState", win.UIA_WindowPatternId, &win.IID_IUIAutomationWindowPattern, func(p *win.IUIAutomationWindowPattern) error {
		return check("WindowVisualState", p.Get_CurrentWindowVisualState(&state))
	})
	return WindowVisualState(state), err
}

// SetWindowVisualState restores, maximizes or minimizes the window (the
// Window pattern).
func (e *Element) SetWindowVisualState(state WindowVisualState) error {
	return withPattern(e, "SetWindowVisualState", win.UIA_WindowPatternId, &win.IID_IUIAutomationWindowPattern, func(p *win.IUIAutomationWindowPattern) error {
		return check("SetWindowVisualState", p.SetWindowVisualState(int32(state)))
	})
}

// IsModal reports whether the window is modal, like a dialog that blocks
// its owner (the Window pattern).
func (e *Element) IsModal() (bool, error) {
	var modal win.BOOL
	err := withPattern(e, "IsModal", win.UIA_WindowPatternId, &win.IID_IUIAutomationWindowPattern, func(p *win.IUIAutomationWindowPattern) error {
		return check("IsModal", p.Get_CurrentIsModal(&modal))
	})
	return modal != win.FALSE, err
}

// ScrollItem pattern

// ScrollIntoView scrolls the container of the element so that the element
// is visible, for example a list item far down a list (the ScrollItem
// pattern).
func (e *Element) ScrollIntoView() error {
	return withPattern(e, "ScrollIntoView", win.UIA_ScrollItemPatternId, &win.IID_IUIAutomationScrollItemPattern, func(p *win.IUIAutomationScrollItemPattern) error {
		return check("ScrollIntoView", p.ScrollIntoView())
	})
}

// LegacyIAccessible pattern

// DefaultAction returns the MSAA default action of the element, such as
// "Press" or "Check", in the language of the system (the LegacyIAccessible
// pattern, which every element based on MSAA or Win32 controls has).
func (e *Element) DefaultAction() (string, error) {
	var action string
	err := withPattern(e, "DefaultAction", win.UIA_LegacyIAccessiblePatternId, &win.IID_IUIAutomationLegacyIAccessiblePattern, func(p *win.IUIAutomationLegacyIAccessiblePattern) error {
		var b *uint16
		if err := check("DefaultAction", p.Get_CurrentDefaultAction(&b)); err != nil {
			return err
		}
		action = bstr(b)
		return nil
	})
	return action, err
}

// DoDefaultAction does the MSAA default action of the element (the
// LegacyIAccessible pattern). It works for elements without the Invoke or
// Toggle pattern, for example some list items.
func (e *Element) DoDefaultAction() error {
	return withPattern(e, "DoDefaultAction", win.UIA_LegacyIAccessiblePatternId, &win.IID_IUIAutomationLegacyIAccessiblePattern, func(p *win.IUIAutomationLegacyIAccessiblePattern) error {
		return check("DoDefaultAction", p.DoDefaultAction())
	})
}

// Description returns the MSAA description of the element, extra text a
// screen reader reads after the name (the LegacyIAccessible pattern).
func (e *Element) Description() (string, error) {
	var description string
	err := withPattern(e, "Description", win.UIA_LegacyIAccessiblePatternId, &win.IID_IUIAutomationLegacyIAccessiblePattern, func(p *win.IUIAutomationLegacyIAccessiblePattern) error {
		var b *uint16
		if err := check("Description", p.Get_CurrentDescription(&b)); err != nil {
			return err
		}
		description = bstr(b)
		return nil
	})
	return description, err
}

// Role returns the MSAA role of the element, such as ROLE_SYSTEM_DIALOG
// (18) for a dialog or ROLE_SYSTEM_PUSHBUTTON (43) for a button (the
// LegacyIAccessible pattern).
func (e *Element) Role() (int, error) {
	var role uint32
	err := withPattern(e, "Role", win.UIA_LegacyIAccessiblePatternId, &win.IID_IUIAutomationLegacyIAccessiblePattern, func(p *win.IUIAutomationLegacyIAccessiblePattern) error {
		return check("Role", p.Get_CurrentRole(&role))
	})
	return int(role), err
}
