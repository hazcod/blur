// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

// Package uia is a client of Microsoft UI Automation: it reads and controls
// the user interface of other programs, the way screen readers and
// automated tests do.
//
// New connects to UI Automation. From the returned Automation, Root,
// ElementFromHandle, ElementFromPoint and FocusedElement return an
// Element, an object in the user interface tree: a window, a button, an
// edit field, a list item and so on. Its methods read the properties
// (Name, ControlType, AutomationID, IsEnabled, BoundingRectangle, ...),
// find other elements below it (FindFirst and FindAll with a Condition)
// and move the keyboard focus to it (SetFocus). A TreeWalker moves through
// the tree from element to element.
//
// Control patterns are what an element can do. Their methods are methods
// of Element and return an error wrapping ErrPatternNotSupported for an
// element without the pattern:
//
//   - Invoke: Invoke, for buttons, menu items and links.
//   - Value: Value, SetValue and IsValueReadOnly, for edit fields and
//     combo boxes.
//   - RangeValue: RangeValue and SetRangeValue, for sliders, progress
//     bars and spinners.
//   - Toggle: ToggleState and Toggle, for check boxes.
//   - ExpandCollapse: ExpandCollapseState, Expand and Collapse, for combo
//     boxes, tree items and menus.
//   - SelectionItem: Select, AddToSelection, RemoveFromSelection,
//     IsSelected and SelectionContainer, for list items, tabs and radio
//     buttons; Selection: Selection and CanSelectMultiple, for their
//     containers.
//   - Text: Text and SelectedText, for documents and edit fields.
//   - Window: CloseWindow, WindowVisualState, SetWindowVisualState and
//     IsModal, for top-level windows.
//   - ScrollItem: ScrollIntoView.
//   - LegacyIAccessible: DefaultAction, DoDefaultAction, Description and Role,
//     the MSAA properties, for elements of Win32 and MSAA programs.
//   - Grid: GridSize and GridItem; GridItem: GridPosition and
//     ContainingGrid, for list views in details view, tables and their
//     cells.
//   - Table: ColumnHeaders, RowHeaders and TableOrder; TableItem:
//     ColumnHeaderItems and RowHeaderItems, the headers screen readers
//     read with a cell.
//   - Scroll: ScrollInfo, Scroll and SetScrollPercent, for lists,
//     documents and other scrollable elements.
//
// PropertyValue reads any property by its PropertyID.
//
// # Caching
//
// Each getter is a call to the program that owns the element. A client
// that reads several properties of many elements, like a screen reader
// reading a list, asks for them at once with a CacheRequest:
// FindFirstBuildCache, FindAllBuildCache and BuildUpdatedCache return
// elements with the requested properties cached, and CachedName,
// CachedControlType, CachedPropertyValue and the other Cached methods read
// them without another call. With a Scope below the element, a cache
// request also caches the elements below it, which CachedChildren and
// CachedParent return.
//
// Events report changes as they happen: OnFocusChanged of the Automation,
// and OnEvent, OnPropertyChanged, OnStructureChanged and OnNotification of
// an Element for the elements in a scope of it. Each returns a
// Subscription, which calls a Go function on a goroutine of its own until
// it is closed.
//
// The package works in any Go program, with or without walk windows.
//
// # Threads
//
// All calls to UI Automation run on one operating system thread that
// belongs to the Automation, in the multithreaded COM apartment, as
// Microsoft recommends for UI Automation clients. Methods may therefore be
// called from any goroutine, also concurrently.
//
// UI Automation gets the information about a window from the thread that
// created it. A call about a window of the calling program therefore
// waits for that window's thread to process messages. Do not call the
// methods of this package from the goroutine that runs the message loop of
// the windows they ask about, for example from a walk event handler about
// windows of the same program: the call cannot finish until UI Automation
// gives up after a timeout. Start a goroutine instead and use
// walk.Synchronize to bring the results back to the user interface.
//
// # Releasing elements
//
// Every Element holds a reference to a COM object, possibly in another
// process. Release frees it at once; otherwise the garbage collector frees
// it some time after the Element becomes unreachable. Close the Automation
// when it is no longer needed; the elements it returned stop working then.
//
// Methods of an element that no longer exists, for example because its
// window was closed, return an error wrapping ErrElementNotAvailable.
package uia
