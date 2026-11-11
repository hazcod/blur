// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package uia

import (
	"fmt"

	"github.com/lpintes/walk/internal/win"
)

// The control patterns of grids and tables, and the Scroll pattern.

// Grid pattern

// GridSize returns the number of rows and columns of a grid, such as a
// list view in details view or a table (the Grid pattern).
func (e *Element) GridSize() (rows, columns int, err error) {
	var r, c int32
	err = withPattern(e, "GridSize", win.UIA_GridPatternId, &win.IID_IUIAutomationGridPattern, func(p *win.IUIAutomationGridPattern) error {
		if err := check("GridSize", p.Get_CurrentRowCount(&r)); err != nil {
			return err
		}
		return check("GridSize", p.Get_CurrentColumnCount(&c))
	})
	return int(r), int(c), err
}

// GridItem returns the cell of a grid at a row and a column, both counted
// from 0 (the Grid pattern).
func (e *Element) GridItem(row, column int) (*Element, error) {
	var item *Element
	err := withPattern(e, "GridItem", win.UIA_GridPatternId, &win.IID_IUIAutomationGridPattern, func(p *win.IUIAutomationGridPattern) error {
		var i *win.IUIAutomationElement
		if err := check("GridItem", p.GetItem(int32(row), int32(column), &i)); err != nil {
			return err
		}
		item = e.a.newElement(i)
		return nil
	})
	return item, err
}

// GridItem pattern

// GridPosition is the position of a cell in a grid, counted from 0.
type GridPosition struct {
	Row        int
	Column     int
	RowSpan    int // the number of rows the cell covers, usually 1
	ColumnSpan int // the number of columns the cell covers, usually 1
}

// GridPosition returns the position of a cell in its grid (the GridItem
// pattern), which screen readers announce as "row 2, column 3".
func (e *Element) GridPosition() (GridPosition, error) {
	var row, column, rowSpan, columnSpan int32
	err := withPattern(e, "GridPosition", win.UIA_GridItemPatternId, &win.IID_IUIAutomationGridItemPattern, func(p *win.IUIAutomationGridItemPattern) error {
		for _, hr := range []win.HRESULT{
			p.Get_CurrentRow(&row),
			p.Get_CurrentColumn(&column),
			p.Get_CurrentRowSpan(&rowSpan),
			p.Get_CurrentColumnSpan(&columnSpan),
		} {
			if err := check("GridPosition", hr); err != nil {
				return err
			}
		}
		return nil
	})
	return GridPosition{Row: int(row), Column: int(column), RowSpan: int(rowSpan), ColumnSpan: int(columnSpan)}, err
}

// ContainingGrid returns the grid a cell belongs to (the GridItem
// pattern).
func (e *Element) ContainingGrid() (*Element, error) {
	var grid *Element
	err := withPattern(e, "ContainingGrid", win.UIA_GridItemPatternId, &win.IID_IUIAutomationGridItemPattern, func(p *win.IUIAutomationGridItemPattern) error {
		var g *win.IUIAutomationElement
		if err := check("ContainingGrid", p.Get_CurrentContainingGrid(&g)); err != nil {
			return err
		}
		grid = e.a.newElement(g)
		return nil
	})
	return grid, err
}

// Table pattern

// TableOrder says whether a table is read by rows or by columns.
type TableOrder int32

const (
	RowMajor                TableOrder = win.RowOrColumnMajor_RowMajor
	ColumnMajor             TableOrder = win.RowOrColumnMajor_ColumnMajor
	TableOrderIndeterminate TableOrder = win.RowOrColumnMajor_Indeterminate
)

// String returns "row major", "column major" or "indeterminate".
func (o TableOrder) String() string {
	switch o {
	case RowMajor:
		return "row major"
	case ColumnMajor:
		return "column major"
	case TableOrderIndeterminate:
		return "indeterminate"
	}
	return fmt.Sprintf("TableOrder(%d)", int32(o))
}

// headers gets the header elements of a table or a table item with get.
func headers[T any, P comPattern[T]](e *Element, op string, id int32, iid *win.IID, get func(P, **win.IUIAutomationElementArray) win.HRESULT) ([]*Element, error) {
	var found []*Element
	err := withPattern(e, op, id, iid, func(p P) error {
		var array *win.IUIAutomationElementArray
		if err := check(op, get(p, &array)); err != nil {
			return err
		}
		var err error
		found, err = e.a.elements(op, array)
		return err
	})
	return found, err
}

// ColumnHeaders returns the column headers of a table, for a list view in
// details view the items of its header (the Table pattern).
func (e *Element) ColumnHeaders() ([]*Element, error) {
	return headers(e, "ColumnHeaders", win.UIA_TablePatternId, &win.IID_IUIAutomationTablePattern, (*win.IUIAutomationTablePattern).GetCurrentColumnHeaders)
}

// RowHeaders returns the row headers of a table, often none (the Table
// pattern).
func (e *Element) RowHeaders() ([]*Element, error) {
	return headers(e, "RowHeaders", win.UIA_TablePatternId, &win.IID_IUIAutomationTablePattern, (*win.IUIAutomationTablePattern).GetCurrentRowHeaders)
}

// TableOrder returns whether the table is read by rows or by columns (the
// Table pattern).
func (e *Element) TableOrder() (TableOrder, error) {
	var order int32
	err := withPattern(e, "TableOrder", win.UIA_TablePatternId, &win.IID_IUIAutomationTablePattern, func(p *win.IUIAutomationTablePattern) error {
		return check("TableOrder", p.Get_CurrentRowOrColumnMajor(&order))
	})
	return TableOrder(order), err
}

// TableItem pattern

// ColumnHeaderItems returns the column headers of a cell of a table,
// which screen readers read before the cell when moving to another column
// (the TableItem pattern).
func (e *Element) ColumnHeaderItems() ([]*Element, error) {
	return headers(e, "ColumnHeaderItems", win.UIA_TableItemPatternId, &win.IID_IUIAutomationTableItemPattern, (*win.IUIAutomationTableItemPattern).GetCurrentColumnHeaderItems)
}

// RowHeaderItems returns the row headers of a cell of a table (the
// TableItem pattern).
func (e *Element) RowHeaderItems() ([]*Element, error) {
	return headers(e, "RowHeaderItems", win.UIA_TableItemPatternId, &win.IID_IUIAutomationTableItemPattern, (*win.IUIAutomationTableItemPattern).GetCurrentRowHeaderItems)
}

// Scroll pattern

// NoScroll is the scroll percent of a direction in which an element
// cannot scroll, in ScrollInfo, and the value for SetScrollPercent that
// leaves a direction as it is.
const NoScroll = -1.0

// ScrollInfo is the scroll position of a list, document or other
// scrollable element.
type ScrollInfo struct {
	// HorizontalPercent and VerticalPercent are the positions, from 0 to
	// 100, or NoScroll.
	HorizontalPercent float64
	VerticalPercent   float64
	// HorizontalViewSize and VerticalViewSize are the visible part, in
	// percent of the whole.
	HorizontalViewSize     float64
	VerticalViewSize       float64
	HorizontallyScrollable bool
	VerticallyScrollable   bool
}

// ScrollInfo returns the scroll position of the element (the Scroll
// pattern).
func (e *Element) ScrollInfo() (ScrollInfo, error) {
	var si ScrollInfo
	err := withPattern(e, "ScrollInfo", win.UIA_ScrollPatternId, &win.IID_IUIAutomationScrollPattern, func(p *win.IUIAutomationScrollPattern) error {
		var h, v win.BOOL
		for _, hr := range []win.HRESULT{
			p.Get_CurrentHorizontalScrollPercent(&si.HorizontalPercent),
			p.Get_CurrentVerticalScrollPercent(&si.VerticalPercent),
			p.Get_CurrentHorizontalViewSize(&si.HorizontalViewSize),
			p.Get_CurrentVerticalViewSize(&si.VerticalViewSize),
			p.Get_CurrentHorizontallyScrollable(&h),
			p.Get_CurrentVerticallyScrollable(&v),
		} {
			if err := check("ScrollInfo", hr); err != nil {
				return err
			}
		}
		si.HorizontallyScrollable = h != win.FALSE
		si.VerticallyScrollable = v != win.FALSE
		return nil
	})
	return si, err
}

// ScrollAmount is a step of Scroll.
type ScrollAmount int32

const (
	ScrollNone           ScrollAmount = win.ScrollAmount_NoAmount
	ScrollSmallDecrement ScrollAmount = win.ScrollAmount_SmallDecrement // like an arrow key up or left
	ScrollSmallIncrement ScrollAmount = win.ScrollAmount_SmallIncrement // like an arrow key down or right
	ScrollLargeDecrement ScrollAmount = win.ScrollAmount_LargeDecrement // like Page Up
	ScrollLargeIncrement ScrollAmount = win.ScrollAmount_LargeIncrement // like Page Down
)

// Scroll scrolls the element by a step in each direction; ScrollNone
// leaves a direction as it is (the Scroll pattern).
func (e *Element) Scroll(horizontal, vertical ScrollAmount) error {
	return withPattern(e, "Scroll", win.UIA_ScrollPatternId, &win.IID_IUIAutomationScrollPattern, func(p *win.IUIAutomationScrollPattern) error {
		return check("Scroll", p.Scroll(int32(horizontal), int32(vertical)))
	})
}

// SetScrollPercent scrolls the element to a position in each direction,
// from 0 to 100; NoScroll leaves a direction as it is (the Scroll
// pattern). On arm64 it is not supported yet and returns an error.
func (e *Element) SetScrollPercent(horizontal, vertical float64) error {
	return withPattern(e, "SetScrollPercent", win.UIA_ScrollPatternId, &win.IID_IUIAutomationScrollPattern, func(p *win.IUIAutomationScrollPattern) error {
		return check("SetScrollPercent", p.SetScrollPercent(horizontal, vertical))
	})
}
