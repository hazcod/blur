// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package uia

import (
	"github.com/lpintes/walk/internal/win"
)

// A TreeWalker moves through the UI Automation tree: from an element to
// its parent, its children and its siblings, skipping the elements that do
// not belong to its view.
type TreeWalker struct {
	a    *Automation
	view int       // index into Automation.walkers, or -1 for cond
	cond Condition // with view -1
}

const (
	viewControl = iota
	viewContent
	viewRaw
)

// ControlView returns a TreeWalker over the control elements, the view
// screen readers use: the controls a user interacts with and the texts
// that label them, without the structure inside them.
func (a *Automation) ControlView() *TreeWalker {
	return &TreeWalker{a: a, view: viewControl}
}

// ContentView returns a TreeWalker over the content elements, the
// control elements that hold information for the user, without labels
// and decorations.
func (a *Automation) ContentView() *TreeWalker {
	return &TreeWalker{a: a, view: viewContent}
}

// RawView returns a TreeWalker over all elements.
func (a *Automation) RawView() *TreeWalker {
	return &TreeWalker{a: a, view: viewRaw}
}

// TreeWalker returns a TreeWalker over the elements that match cond.
func (a *Automation) TreeWalker(cond Condition) *TreeWalker {
	return &TreeWalker{a: a, view: -1, cond: cond}
}

// walker runs f with the COM tree walker on the thread of the Automation.
func (w *TreeWalker) walker(f func(walker *win.IUIAutomationTreeWalker) error) error {
	a := w.a
	if w.view < 0 {
		c, err := createCondition(a.uia, w.cond)
		if err != nil {
			return err
		}
		defer c.Release()
		var walker *win.IUIAutomationTreeWalker
		if hr := a.uia.CreateTreeWalker(c, &walker); win.FAILED(hr) {
			return hresultError("CreateTreeWalker", hr)
		}
		defer walker.Release()
		return f(walker)
	}

	if a.walkers[w.view] == nil {
		var walker *win.IUIAutomationTreeWalker
		var hr win.HRESULT
		switch w.view {
		case viewControl:
			hr = a.uia.Get_ControlViewWalker(&walker)
		case viewContent:
			hr = a.uia.Get_ContentViewWalker(&walker)
		default:
			hr = a.uia.Get_RawViewWalker(&walker)
		}
		if win.FAILED(hr) {
			return hresultError("get tree walker", hr)
		}
		a.walkers[w.view] = walker
	}
	return f(a.walkers[w.view])
}

// move runs a navigation method of the COM tree walker for e.
func (w *TreeWalker) move(op string, e *Element, method func(walker *win.IUIAutomationTreeWalker, element *win.IUIAutomationElement, result **win.IUIAutomationElement) win.HRESULT) (*Element, error) {
	if e == nil {
		return nil, errNilElement
	}
	if e.a != w.a {
		return nil, errOtherAutomation
	}
	var result *Element
	err := e.call(func(p *win.IUIAutomationElement) error {
		return w.walker(func(walker *win.IUIAutomationTreeWalker) error {
			var r *win.IUIAutomationElement
			if hr := method(walker, p, &r); win.FAILED(hr) {
				return hresultError(op, hr)
			}
			result = w.a.newElement(r)
			return nil
		})
	})
	return result, err
}

// Parent returns the parent of e in the view, or nil for the root element.
func (w *TreeWalker) Parent(e *Element) (*Element, error) {
	return w.move("GetParentElement", e, (*win.IUIAutomationTreeWalker).GetParentElement)
}

// FirstChild returns the first child of e in the view, or nil if it has
// none.
func (w *TreeWalker) FirstChild(e *Element) (*Element, error) {
	return w.move("GetFirstChildElement", e, (*win.IUIAutomationTreeWalker).GetFirstChildElement)
}

// LastChild returns the last child of e in the view, or nil if it has
// none.
func (w *TreeWalker) LastChild(e *Element) (*Element, error) {
	return w.move("GetLastChildElement", e, (*win.IUIAutomationTreeWalker).GetLastChildElement)
}

// NextSibling returns the next sibling of e in the view, or nil if it is
// the last one.
func (w *TreeWalker) NextSibling(e *Element) (*Element, error) {
	return w.move("GetNextSiblingElement", e, (*win.IUIAutomationTreeWalker).GetNextSiblingElement)
}

// PreviousSibling returns the previous sibling of e in the view, or nil if
// it is the first one.
func (w *TreeWalker) PreviousSibling(e *Element) (*Element, error) {
	return w.move("GetPreviousSiblingElement", e, (*win.IUIAutomationTreeWalker).GetPreviousSiblingElement)
}

// Children returns the children of e in the view, in order.
func (w *TreeWalker) Children(e *Element) ([]*Element, error) {
	var children []*Element
	child, err := w.FirstChild(e)
	for child != nil && err == nil {
		children = append(children, child)
		child, err = w.NextSibling(child)
	}
	if err != nil {
		for _, c := range children {
			c.Release()
		}
		return nil, err
	}
	return children, nil
}
