// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package uia

import (
	"errors"
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/lpintes/walk/internal/com"
	"github.com/lpintes/walk/internal/win"
)

// EventID identifies a UI Automation event for OnEvent.
type EventID int32

const (
	EventToolTipOpened               EventID = win.UIA_ToolTipOpenedEventId
	EventToolTipClosed               EventID = win.UIA_ToolTipClosedEventId
	EventMenuOpened                  EventID = win.UIA_MenuOpenedEventId
	EventMenuClosed                  EventID = win.UIA_MenuClosedEventId
	EventMenuModeStart               EventID = win.UIA_MenuModeStartEventId
	EventMenuModeEnd                 EventID = win.UIA_MenuModeEndEventId
	EventAsyncContentLoaded          EventID = win.UIA_AsyncContentLoadedEventId
	EventInvoked                     EventID = win.UIA_Invoke_InvokedEventId
	EventElementSelected             EventID = win.UIA_SelectionItem_ElementSelectedEventId
	EventElementAddedToSelection     EventID = win.UIA_SelectionItem_ElementAddedToSelectionEventId
	EventElementRemovedFromSelection EventID = win.UIA_SelectionItem_ElementRemovedFromSelectionEventId
	EventSelectionInvalidated        EventID = win.UIA_Selection_InvalidatedEventId
	EventTextSelectionChanged        EventID = win.UIA_Text_TextSelectionChangedEventId
	EventTextChanged                 EventID = win.UIA_Text_TextChangedEventId
	EventWindowOpened                EventID = win.UIA_Window_WindowOpenedEventId
	EventWindowClosed                EventID = win.UIA_Window_WindowClosedEventId
	EventSystemAlert                 EventID = win.UIA_SystemAlertEventId
	EventLiveRegionChanged           EventID = win.UIA_LiveRegionChangedEventId
)

var eventNames = map[EventID]string{
	EventToolTipOpened:               "ToolTipOpened",
	EventToolTipClosed:               "ToolTipClosed",
	EventMenuOpened:                  "MenuOpened",
	EventMenuClosed:                  "MenuClosed",
	EventMenuModeStart:               "MenuModeStart",
	EventMenuModeEnd:                 "MenuModeEnd",
	EventAsyncContentLoaded:          "AsyncContentLoaded",
	EventInvoked:                     "Invoked",
	EventElementSelected:             "ElementSelected",
	EventElementAddedToSelection:     "ElementAddedToSelection",
	EventElementRemovedFromSelection: "ElementRemovedFromSelection",
	EventSelectionInvalidated:        "SelectionInvalidated",
	EventTextSelectionChanged:        "TextSelectionChanged",
	EventTextChanged:                 "TextChanged",
	EventWindowOpened:                "WindowOpened",
	EventWindowClosed:                "WindowClosed",
	EventSystemAlert:                 "SystemAlert",
	EventLiveRegionChanged:           "LiveRegionChanged",
}

// String returns the name of the event, such as "Invoked", or
// "EventID(N)" for an unknown value.
func (id EventID) String() string {
	if name, ok := eventNames[id]; ok {
		return name
	}
	return fmt.Sprintf("EventID(%d)", int32(id))
}

// StructureChange says how the tree below an element changed.
type StructureChange int32

const (
	ChildAdded          StructureChange = win.StructureChangeType_ChildAdded
	ChildRemoved        StructureChange = win.StructureChangeType_ChildRemoved
	ChildrenInvalidated StructureChange = win.StructureChangeType_ChildrenInvalidated
	ChildrenBulkAdded   StructureChange = win.StructureChangeType_ChildrenBulkAdded
	ChildrenBulkRemoved StructureChange = win.StructureChangeType_ChildrenBulkRemoved
	ChildrenReordered   StructureChange = win.StructureChangeType_ChildrenReordered
)

// String returns the name of the change, such as "ChildAdded".
func (c StructureChange) String() string {
	switch c {
	case ChildAdded:
		return "ChildAdded"
	case ChildRemoved:
		return "ChildRemoved"
	case ChildrenInvalidated:
		return "ChildrenInvalidated"
	case ChildrenBulkAdded:
		return "ChildrenBulkAdded"
	case ChildrenBulkRemoved:
		return "ChildrenBulkRemoved"
	case ChildrenReordered:
		return "ChildrenReordered"
	}
	return fmt.Sprintf("StructureChange(%d)", int32(c))
}

// NotificationKind says what a notification is about.
type NotificationKind int32

const (
	NotificationItemAdded       NotificationKind = win.NotificationKind_ItemAdded
	NotificationItemRemoved     NotificationKind = win.NotificationKind_ItemRemoved
	NotificationActionCompleted NotificationKind = win.NotificationKind_ActionCompleted
	NotificationActionAborted   NotificationKind = win.NotificationKind_ActionAborted
	NotificationOther           NotificationKind = win.NotificationKind_Other
)

// String returns the name of the kind, such as "ActionCompleted".
func (k NotificationKind) String() string {
	switch k {
	case NotificationItemAdded:
		return "ItemAdded"
	case NotificationItemRemoved:
		return "ItemRemoved"
	case NotificationActionCompleted:
		return "ActionCompleted"
	case NotificationActionAborted:
		return "ActionAborted"
	case NotificationOther:
		return "Other"
	}
	return fmt.Sprintf("NotificationKind(%d)", int32(k))
}

// NotificationProcessing says how a screen reader should treat a
// notification relative to other speech. walk.Announce takes the same
// values as walk.AnnouncementProcessing.
type NotificationProcessing int32

const (
	ProcessingImportantAll          NotificationProcessing = win.NotificationProcessing_ImportantAll
	ProcessingImportantMostRecent   NotificationProcessing = win.NotificationProcessing_ImportantMostRecent
	ProcessingAll                   NotificationProcessing = win.NotificationProcessing_All
	ProcessingMostRecent            NotificationProcessing = win.NotificationProcessing_MostRecent
	ProcessingCurrentThenMostRecent NotificationProcessing = win.NotificationProcessing_CurrentThenMostRecent
)

// String returns the name of the processing, such as "MostRecent".
func (p NotificationProcessing) String() string {
	switch p {
	case ProcessingImportantAll:
		return "ImportantAll"
	case ProcessingImportantMostRecent:
		return "ImportantMostRecent"
	case ProcessingAll:
		return "All"
	case ProcessingMostRecent:
		return "MostRecent"
	case ProcessingCurrentThenMostRecent:
		return "CurrentThenMostRecent"
	}
	return fmt.Sprintf("NotificationProcessing(%d)", int32(p))
}

// Notification is a text a program asks screen readers to speak, for
// example with walk.Announce.
type Notification struct {
	Kind       NotificationKind
	Processing NotificationProcessing
	Text       string
	ActivityID string // identifies the kind of message, chosen by the program
}

// A Subscription delivers UI Automation events to a Go function until it
// is closed.
//
// UI Automation reports events on threads of its own. A Subscription
// queues them and calls the function on a goroutine of its own, one event
// at a time, in the order they came, so the function may take its time and
// may call any method of the package, including Close of the
// Subscription. The elements passed to the function belong to it, like
// the ones the other methods return.
type Subscription struct {
	a   *Automation
	obj *com.Object

	// Set on the thread of the Automation while the handler is
	// registered.
	remove  func() win.HRESULT
	element *win.IUIAutomationElement // with a reference added, or nil

	// One of automationEvent, focusChangedEvent, propertyChangedEvent,
	// structureChangedEvent and notificationEvent.
	handler any

	mu     sync.Mutex
	closed bool
	queue  []func()
	wake   chan struct{}
	done   chan struct{}
}

func newSubscription(a *Automation) *Subscription {
	return &Subscription{
		a:    a,
		wake: make(chan struct{}, 1),
		done: make(chan struct{}),
	}
}

// post queues f for the delivery goroutine. It runs on a thread of UI
// Automation and must not block.
func (s *Subscription) post(f func()) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.queue = append(s.queue, f)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Subscription) deliver() {
	for {
		select {
		case <-s.wake:
		case <-s.done:
			return
		}
		s.mu.Lock()
		queue := s.queue
		s.queue = nil
		s.mu.Unlock()
		for _, f := range queue {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return
			}
			f()
		}
	}
}

// stop ends the delivery and reports whether s was open.
func (s *Subscription) stop() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.closed = true
	s.queue = nil
	close(s.done)
	return true
}

// Close removes the event handler. The function of the Subscription is not
// called again, except that a call already running finishes. Close returns
// nil, also when called again or after Close of the Automation.
func (s *Subscription) Close() error {
	if !s.stop() {
		return nil
	}
	err := s.a.do(func() error {
		return s.a.unsubscribe(s)
	})
	if errors.Is(err, ErrClosed) {
		// Close of the Automation removes every handler.
		return nil
	}
	return err
}

// unsubscribe removes the handler of s if it is still registered. It runs
// on the thread of the Automation.
func (a *Automation) unsubscribe(s *Subscription) error {
	if _, ok := a.subscriptions[s]; !ok {
		return nil
	}
	delete(a.subscriptions, s)
	hr := s.remove()
	if s.element != nil {
		s.element.Release()
	}
	s.obj.Release()
	return check("remove event handler", hr)
}

// subscribe creates the COM handler of s with vtable and registers it with
// add, which runs on the thread of the Automation and returns the
// function that removes it.
func (a *Automation) subscribe(s *Subscription, vtable *com.VTable, element *Element, add func(handler unsafe.Pointer, element *win.IUIAutomationElement) (remove func() win.HRESULT, hr win.HRESULT)) (*Subscription, error) {
	s.obj = com.NewObject(s, vtable)
	register := func() error {
		var p *win.IUIAutomationElement
		if element != nil {
			if element.p == nil {
				return ErrReleased
			}
			p = element.p
		}
		remove, hr := add(s.obj.Interface(0), p)
		if win.FAILED(hr) {
			return hresultError("add event handler", hr)
		}
		if p != nil {
			p.AddRef()
		}
		s.element = p
		s.remove = remove
		if a.subscriptions == nil {
			a.subscriptions = map[*Subscription]struct{}{}
		}
		a.subscriptions[s] = struct{}{}
		return nil
	}
	var err error
	if element != nil {
		if element.a != a {
			err = errOtherAutomation
		} else {
			err = a.do(register)
		}
	} else {
		err = a.do(register)
	}
	if err != nil {
		s.obj.Release()
		return nil, err
	}
	go s.deliver()
	return s, nil
}

// senderElement wraps the sender of an event, which UI Automation lends to
// the handler, in an Element of its own. It runs on a thread of UI
// Automation.
func (a *Automation) senderElement(sender *win.IUIAutomationElement) *Element {
	if sender == nil {
		return nil
	}
	sender.AddRef()
	return a.newElement(sender)
}

func subscriptionOf(this *com.This) *Subscription {
	return this.Object().Impl().(*Subscription)
}

// The vtables of the handlers, created once (see package com).
var (
	vtablesOnce              sync.Once
	automationEventVTable    *com.VTable
	focusChangedVTable       *com.VTable
	propertyChangedVTable    *com.VTable
	structureChangedVTable   *com.VTable
	notificationEventsVTable *com.VTable
)

func initVTables() {
	vtablesOnce.Do(func() {
		automationEventVTable = com.NewVTable(&win.IUIAutomationEventHandlerVtbl{
			HandleAutomationEvent: syscall.NewCallback(handleAutomationEvent),
		}, &win.IID_IUIAutomationEventHandler)
		focusChangedVTable = com.NewVTable(&win.IUIAutomationFocusChangedEventHandlerVtbl{
			HandleFocusChangedEvent: syscall.NewCallback(handleFocusChangedEvent),
		}, &win.IID_IUIAutomationFocusChangedEventHandler)
		propertyChangedVTable = com.NewVTable(&win.IUIAutomationPropertyChangedEventHandlerVtbl{
			HandlePropertyChangedEvent: syscall.NewCallback(handlePropertyChangedEvent),
		}, &win.IID_IUIAutomationPropertyChangedEventHandler)
		structureChangedVTable = com.NewVTable(&win.IUIAutomationStructureChangedEventHandlerVtbl{
			HandleStructureChangedEvent: syscall.NewCallback(handleStructureChangedEvent),
		}, &win.IID_IUIAutomationStructureChangedEventHandler)
		notificationEventsVTable = com.NewVTable(&win.IUIAutomationNotificationEventHandlerVtbl{
			HandleNotificationEvent: syscall.NewCallback(handleNotificationEvent),
		}, &win.IID_IUIAutomationNotificationEventHandler)
	})
}

// Automation events

type automationEvent struct {
	f func(sender *Element, id EventID)
}

func handleAutomationEvent(this *com.This, sender *win.IUIAutomationElement, eventID int32) uintptr {
	s := subscriptionOf(this)
	ev := s.handler.(automationEvent)
	e := s.a.senderElement(sender)
	s.post(func() { ev.f(e, EventID(eventID)) })
	return win.S_OK
}

// OnEvent calls f for each event id that UI Automation reports for
// elements in scope of e, with the element the event is about. Close the
// returned Subscription to stop.
func (e *Element) OnEvent(id EventID, scope Scope, f func(sender *Element, id EventID)) (*Subscription, error) {
	if e == nil {
		return nil, errNilElement
	}
	initVTables()
	s := newSubscription(e.a)
	s.handler = automationEvent{f: f}
	a := e.a
	return a.subscribe(s, automationEventVTable, e, func(h unsafe.Pointer, p *win.IUIAutomationElement) (func() win.HRESULT, win.HRESULT) {
		handler := (*win.IUIAutomationEventHandler)(h)
		hr := a.uia.AddAutomationEventHandler(int32(id), p, int32(scope), nil, handler)
		return func() win.HRESULT { return a.uia.RemoveAutomationEventHandler(int32(id), p, handler) }, hr
	})
}

// Focus changed events

type focusChangedEvent struct {
	f func(focused *Element)
}

func handleFocusChangedEvent(this *com.This, sender *win.IUIAutomationElement) uintptr {
	s := subscriptionOf(this)
	ev := s.handler.(focusChangedEvent)
	e := s.a.senderElement(sender)
	s.post(func() { ev.f(e) })
	return win.S_OK
}

// OnFocusChanged calls f each time the keyboard focus moves, anywhere on
// the desktop, with the element that got it. Close the returned
// Subscription to stop.
func (a *Automation) OnFocusChanged(f func(focused *Element)) (*Subscription, error) {
	initVTables()
	s := newSubscription(a)
	s.handler = focusChangedEvent{f: f}
	return a.subscribe(s, focusChangedVTable, nil, func(h unsafe.Pointer, _ *win.IUIAutomationElement) (func() win.HRESULT, win.HRESULT) {
		handler := (*win.IUIAutomationFocusChangedEventHandler)(h)
		hr := a.uia.AddFocusChangedEventHandler(nil, handler)
		return func() win.HRESULT { return a.uia.RemoveFocusChangedEventHandler(handler) }, hr
	})
}

// Property changed events

type propertyChangedEvent struct {
	f func(sender *Element, id PropertyID, value any)
}

// handlePropertyChanged is called by the propertyChangedCallback of the
// architecture with the new value, which UI Automation owns.
func handlePropertyChanged(this *com.This, sender *win.IUIAutomationElement, propertyID int32, v *win.VARIANT) uintptr {
	s := subscriptionOf(this)
	ev := s.handler.(propertyChangedEvent)
	e := s.a.senderElement(sender)
	id := PropertyID(propertyID)
	value := variantValue(id, v)
	s.post(func() { ev.f(e, id, value) })
	return win.S_OK
}

// variantValue converts a property value to the Go types listed next to
// the PropertyID constants. Other values become nil.
func variantValue(id PropertyID, v *win.VARIANT) any {
	switch v.Vt {
	case win.VT_BSTR:
		b, _ := v.BSTR()
		return windows.UTF16PtrToString(b)
	case win.VT_BOOL:
		b, _ := v.Bool()
		return b != win.VARIANT_FALSE
	case win.VT_I4:
		n, _ := v.Long()
		if id == PropertyControlType {
			return ControlType(n)
		}
		return int(n)
	case win.VT_R8:
		f, _ := v.Double()
		return f
	}
	return nil
}

// OnPropertyChanged calls f each time one of the properties ids changes
// for an element in scope of e, with the element, the property and its
// new value. The value has the Go type listed next to the PropertyID
// constant, float64 for floating-point properties, and is nil for
// properties of other types. It is also nil when UI Automation sends no
// value, as the proxies of Win32 controls do for most properties (the
// name is an exception); read the property from the element then. Not
// every change raises an event: the Win32 proxies raise none for
// IsEnabled, for example. Close the returned Subscription to stop.
func (e *Element) OnPropertyChanged(scope Scope, f func(sender *Element, id PropertyID, value any), ids ...PropertyID) (*Subscription, error) {
	if e == nil {
		return nil, errNilElement
	}
	if len(ids) == 0 {
		return nil, errors.New("uia: OnPropertyChanged: no property")
	}
	initVTables()
	s := newSubscription(e.a)
	s.handler = propertyChangedEvent{f: f}
	a := e.a
	properties := make([]int32, len(ids))
	for i, id := range ids {
		properties[i] = int32(id)
	}
	return a.subscribe(s, propertyChangedVTable, e, func(h unsafe.Pointer, p *win.IUIAutomationElement) (func() win.HRESULT, win.HRESULT) {
		handler := (*win.IUIAutomationPropertyChangedEventHandler)(h)
		hr := a.uia.AddPropertyChangedEventHandlerNativeArray(p, int32(scope), nil, handler, &properties[0], int32(len(properties)))
		return func() win.HRESULT { return a.uia.RemovePropertyChangedEventHandler(p, handler) }, hr
	})
}

// Structure changed events

type structureChangedEvent struct {
	f func(sender *Element, change StructureChange)
}

func handleStructureChangedEvent(this *com.This, sender *win.IUIAutomationElement, changeType int32, runtimeID *win.SAFEARRAY) uintptr {
	s := subscriptionOf(this)
	ev := s.handler.(structureChangedEvent)
	e := s.a.senderElement(sender)
	s.post(func() { ev.f(e, StructureChange(changeType)) })
	return win.S_OK
}

// OnStructureChanged calls f each time elements are added to or removed
// from the tree in scope of e. For ChildAdded the element is the new
// child; for the other changes it is the parent. Close the returned
// Subscription to stop.
func (e *Element) OnStructureChanged(scope Scope, f func(sender *Element, change StructureChange)) (*Subscription, error) {
	if e == nil {
		return nil, errNilElement
	}
	initVTables()
	s := newSubscription(e.a)
	s.handler = structureChangedEvent{f: f}
	a := e.a
	return a.subscribe(s, structureChangedVTable, e, func(h unsafe.Pointer, p *win.IUIAutomationElement) (func() win.HRESULT, win.HRESULT) {
		handler := (*win.IUIAutomationStructureChangedEventHandler)(h)
		hr := a.uia.AddStructureChangedEventHandler(p, int32(scope), nil, handler)
		return func() win.HRESULT { return a.uia.RemoveStructureChangedEventHandler(p, handler) }, hr
	})
}

// Notification events

type notificationEvent struct {
	f func(sender *Element, n Notification)
}

func handleNotificationEvent(this *com.This, sender *win.IUIAutomationElement, kind, processing int32, displayString, activityID *uint16) uintptr {
	s := subscriptionOf(this)
	ev := s.handler.(notificationEvent)
	e := s.a.senderElement(sender)
	n := Notification{
		Kind:       NotificationKind(kind),
		Processing: NotificationProcessing(processing),
		Text:       windows.UTF16PtrToString(displayString),
		ActivityID: windows.UTF16PtrToString(activityID),
	}
	s.post(func() { ev.f(e, n) })
	return win.S_OK
}

// OnNotification calls f for each notification raised by an element in
// scope of e: texts a program asks screen readers to speak, for example
// with walk.Announce. It needs Windows 10 version 1709 or later. Close the
// returned Subscription to stop.
func (e *Element) OnNotification(scope Scope, f func(sender *Element, n Notification)) (*Subscription, error) {
	if e == nil {
		return nil, errNilElement
	}
	initVTables()
	s := newSubscription(e.a)
	s.handler = notificationEvent{f: f}
	a := e.a
	return a.subscribe(s, notificationEventsVTable, e, func(h unsafe.Pointer, p *win.IUIAutomationElement) (func() win.HRESULT, win.HRESULT) {
		uia5, hr := a.automation5()
		if win.FAILED(hr) {
			return nil, hr
		}
		handler := (*win.IUIAutomationNotificationEventHandler)(h)
		hr = uia5.AddNotificationEventHandler(p, int32(scope), nil, handler)
		return func() win.HRESULT { return uia5.RemoveNotificationEventHandler(p, handler) }, hr
	})
}

// automation5 returns IUIAutomation5 of the Automation, which has the
// notification events. It runs on the thread of the Automation.
func (a *Automation) automation5() (*win.IUIAutomation5, win.HRESULT) {
	if a.uia5 == nil {
		var p unsafe.Pointer
		if hr := a.uia.QueryInterface((*syscall.GUID)(&win.IID_IUIAutomation5), &p); win.FAILED(hr) {
			return nil, hr
		}
		a.uia5 = (*win.IUIAutomation5)(p)
	}
	return a.uia5, win.S_OK
}
