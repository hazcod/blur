// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

// Package com implements COM objects in Go.
//
// A COM object is an [Object] that exposes one or more interfaces. Each
// interface is described by a [VTable], which holds the method callbacks
// and the interface IDs that QueryInterface answers with it. Package com
// implements the IUnknown methods (QueryInterface, AddRef and Release) of
// every vtable; the other methods are Go functions turned into callbacks
// with [syscall.NewCallback]. Their first parameter is the interface
// pointer, declared as *[This]:
//
//	func site_GetWindow(this *com.This, phwnd *win.HWND) uintptr {
//		*phwnd = this.Object().Impl().(*Widget).hWnd
//		return win.S_OK
//	}
//
// Create vtables once and keep them for the lifetime of the program:
// [syscall.NewCallback] can only create a limited number of callbacks,
// which are never freed.
//
// An Object is reference counted. While its count is above zero, the
// memory COM points to is pinned with [runtime.Pinner], and the object
// keeps its implementation reachable, so COM never holds a pointer to
// memory the garbage collector may free. The creator holds the first
// reference and gives it up with [Object.Release] when it no longer needs
// the object; the object is released for good when COM has released all
// references too.
//
// COM calls these methods on the thread that created the object (walk
// uses single-threaded apartments), but reference counting is atomic
// anyway.
package com

import (
	"reflect"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/lpintes/walk/internal/win"
)

// This is the memory an interface pointer of an Object points to. Like
// every COM interface, it starts with a pointer to the vtable.
type This struct {
	vtbl   unsafe.Pointer
	object *Object
}

// Object returns the object this interface pointer belongs to.
func (this *This) Object() *Object {
	return this.object
}

// VTable is the vtable of one COM interface implemented in Go.
type VTable struct {
	methods unsafe.Pointer
	iids    []*win.IID
}

var (
	unknownOnce     sync.Once
	queryInterface  uintptr
	addRef          uintptr
	release         uintptr
	vtablesMutex    sync.Mutex
	vtablesPinner   runtime.Pinner // never unpinned
	errNotPtrStruct = "com: NewVTable needs a pointer to a vtable struct of uintptr fields"
)

// NewVTable returns a vtable for the methods in vtbl, which must point to
// a vtable struct of the win package, such as *win.IOleClientSiteVtbl.
//
// Its first three fields are QueryInterface, AddRef and Release; NewVTable
// sets them. Every other field must hold a callback created with
// [syscall.NewCallback]. NewVTable panics if vtbl is not such a struct or
// if a method is missing.
//
// QueryInterface answers with this vtable for the interface IDs in iids,
// which should include the IDs of the base interfaces except IUnknown.
// It answers IUnknown with the first vtable of the object.
func NewVTable(vtbl any, iids ...*win.IID) *VTable {
	v := reflect.ValueOf(vtbl)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		panic(errNotPtrStruct)
	}
	s := v.Elem()
	t := s.Type()
	if t.NumField() < 3 ||
		t.Field(0).Name != "QueryInterface" ||
		t.Field(1).Name != "AddRef" ||
		t.Field(2).Name != "Release" {
		panic(errNotPtrStruct)
	}
	for i := range t.NumField() {
		if t.Field(i).Type.Kind() != reflect.Uintptr {
			panic(errNotPtrStruct)
		}
		if i >= 3 && s.Field(i).Uint() == 0 {
			panic("com: NewVTable: method " + t.Name() + "." + t.Field(i).Name + " is missing")
		}
	}

	unknownOnce.Do(func() {
		queryInterface = syscall.NewCallback(unknownQueryInterface)
		addRef = syscall.NewCallback(unknownAddRef)
		release = syscall.NewCallback(unknownRelease)
	})
	s.Field(0).SetUint(uint64(queryInterface))
	s.Field(1).SetUint(uint64(addRef))
	s.Field(2).SetUint(uint64(release))

	methods := v.UnsafePointer()

	vtablesMutex.Lock()
	vtablesPinner.Pin(methods)
	vtablesMutex.Unlock()

	return &VTable{
		methods: methods,
		iids:    slices.Clone(iids),
	}
}

// Object is a COM object implemented in Go.
type Object struct {
	refs    atomic.Int32
	impl    any
	slots   []This
	vtables []*VTable
	pinner  runtime.Pinner
}

// liveObjects holds every object whose reference count is above zero.
// Pinning keeps an object from being freed, but not the memory it points
// to, such as its pinner and implementation, so live objects must stay
// reachable even when their creator has dropped them.
var (
	liveMutex   sync.Mutex
	liveObjects = make(map[*Object]struct{})
)

// NewObject returns an object that exposes one interface for each vtable
// and has a reference count of 1. impl is the Go value that implements
// the object; the methods get it with this.Object().Impl().
func NewObject(impl any, vtables ...*VTable) *Object {
	if len(vtables) == 0 {
		panic("com: NewObject needs at least one vtable")
	}

	o := &Object{
		impl:    impl,
		slots:   make([]This, len(vtables)),
		vtables: slices.Clone(vtables),
	}
	for i, vt := range vtables {
		o.slots[i] = This{vtbl: vt.methods, object: o}
	}
	o.refs.Store(1)

	o.pinner.Pin(o)
	o.pinner.Pin(&o.slots[0])

	liveMutex.Lock()
	liveObjects[o] = struct{}{}
	liveMutex.Unlock()

	return o
}

// Impl returns the Go value that implements the object.
func (o *Object) Impl() any {
	return o.impl
}

// Interface returns the interface pointer for the i-th vtable passed to
// NewObject, without adding a reference. Convert it to the interface type
// of the win package, for example (*win.IOleClientSite)(o.Interface(0)).
func (o *Object) Interface(i int) unsafe.Pointer {
	return unsafe.Pointer(&o.slots[i])
}

// AddRef adds a reference and returns the new reference count.
func (o *Object) AddRef() uint32 {
	return uint32(o.refs.Add(1))
}

// Release removes a reference and returns the new reference count. When
// it reaches zero, the object is unpinned and COM must no longer use it.
func (o *Object) Release() uint32 {
	n := o.refs.Add(-1)
	if n == 0 {
		o.pinner.Unpin()

		liveMutex.Lock()
		delete(liveObjects, o)
		liveMutex.Unlock()
	} else if n < 0 {
		// A client released more references than it had. Panicking in
		// a callback would end the program, so ignore it.
		o.refs.Store(0)
		return 0
	}
	return uint32(n)
}

// QueryInterface returns in ppv the interface pointer for riid with a
// reference added and returns S_OK, or returns E_NOINTERFACE. The result
// is an HRESULT as a callback returns it.
func (o *Object) QueryInterface(riid win.REFIID, ppv *unsafe.Pointer) uintptr {
	if ppv == nil {
		return win.E_POINTER
	}
	*ppv = nil
	if riid == nil {
		return win.E_INVALIDARG
	}

	slot := -1
	if win.EqualREFIID(riid, &win.IID_IUnknown) {
		slot = 0
	} else {
	search:
		for i, vt := range o.vtables {
			for _, iid := range vt.iids {
				if win.EqualREFIID(riid, iid) {
					slot = i
					break search
				}
			}
		}
	}
	if slot < 0 {
		return win.E_NOINTERFACE
	}

	o.AddRef()
	*ppv = o.Interface(slot)

	return win.S_OK
}

func unknownQueryInterface(this *This, riid win.REFIID, ppv *unsafe.Pointer) uintptr {
	return this.object.QueryInterface(riid, ppv)
}

func unknownAddRef(this *This) uintptr {
	return uintptr(this.object.AddRef())
}

func unknownRelease(this *This) uintptr {
	return uintptr(this.object.Release())
}
