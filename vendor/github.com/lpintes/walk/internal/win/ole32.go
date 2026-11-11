// Copyright 2010 The win Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package win

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type IID syscall.GUID
type CLSID syscall.GUID
type REFIID *IID
type REFCLSID *CLSID

func EqualREFIID(a, b REFIID) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil {
		return false
	}

	if a.Data1 != b.Data1 || a.Data2 != b.Data2 || a.Data3 != b.Data3 {
		return false
	}

	for i := range 8 {
		if a.Data4[i] != b.Data4[i] {
			return false
		}
	}

	return true
}

func (cf *IClassFactory) Release() uint32 {
	ret, _, _ := syscall.SyscallN(cf.LpVtbl.Release,
		uintptr(unsafe.Pointer(cf)))

	return uint32(ret)
}

func (cf *IClassFactory) CreateInstance(pUnkOuter *IUnknown, riid REFIID, ppvObject *unsafe.Pointer) HRESULT {
	ret, _, _ := syscall.SyscallN(cf.LpVtbl.CreateInstance,
		uintptr(unsafe.Pointer(cf)),
		uintptr(unsafe.Pointer(pUnkOuter)),
		uintptr(unsafe.Pointer(riid)),
		uintptr(unsafe.Pointer(ppvObject)))

	return HRESULT(ret)
}

func (cp *IConnectionPoint) Release() uint32 {
	ret, _, _ := syscall.SyscallN(cp.LpVtbl.Release,
		uintptr(unsafe.Pointer(cp)))

	return uint32(ret)
}

func (cp *IConnectionPoint) Advise(pUnkSink unsafe.Pointer, pdwCookie *uint32) HRESULT {
	ret, _, _ := syscall.SyscallN(cp.LpVtbl.Advise,
		uintptr(unsafe.Pointer(cp)),
		uintptr(pUnkSink),
		uintptr(unsafe.Pointer(pdwCookie)))

	return HRESULT(ret)
}

func (cp *IConnectionPoint) Unadvise(dwCookie uint32) HRESULT {
	ret, _, _ := syscall.SyscallN(cp.LpVtbl.Unadvise,
		uintptr(unsafe.Pointer(cp)),
		uintptr(dwCookie))

	return HRESULT(ret)
}

func (cpc *IConnectionPointContainer) Release() uint32 {
	ret, _, _ := syscall.SyscallN(cpc.LpVtbl.Release,
		uintptr(unsafe.Pointer(cpc)))

	return uint32(ret)
}

func (cpc *IConnectionPointContainer) FindConnectionPoint(riid REFIID, ppCP **IConnectionPoint) HRESULT {
	ret, _, _ := syscall.SyscallN(cpc.LpVtbl.FindConnectionPoint,
		uintptr(unsafe.Pointer(cpc)),
		uintptr(unsafe.Pointer(riid)),
		uintptr(unsafe.Pointer(ppCP)))

	return HRESULT(ret)
}

func (obj *IOleInPlaceObject) Release() uint32 {
	ret, _, _ := syscall.SyscallN(obj.LpVtbl.Release,
		uintptr(unsafe.Pointer(obj)))

	return uint32(ret)
}

func (obj *IOleInPlaceObject) SetObjectRects(lprcPosRect, lprcClipRect *RECT) HRESULT {
	ret, _, _ := syscall.SyscallN(obj.LpVtbl.SetObjectRects,
		uintptr(unsafe.Pointer(obj)),
		uintptr(unsafe.Pointer(lprcPosRect)),
		uintptr(unsafe.Pointer(lprcClipRect)))

	return HRESULT(ret)
}

func (obj *IOleObject) QueryInterface(riid REFIID, ppvObject *unsafe.Pointer) HRESULT {
	ret, _, _ := syscall.SyscallN(obj.LpVtbl.QueryInterface,
		uintptr(unsafe.Pointer(obj)),
		uintptr(unsafe.Pointer(riid)),
		uintptr(unsafe.Pointer(ppvObject)))

	return HRESULT(ret)
}

func (obj *IOleObject) Release() uint32 {
	ret, _, _ := syscall.SyscallN(obj.LpVtbl.Release,
		uintptr(unsafe.Pointer(obj)))

	return uint32(ret)
}

func (obj *IOleObject) SetClientSite(pClientSite *IOleClientSite) HRESULT {
	ret, _, _ := syscall.SyscallN(obj.LpVtbl.SetClientSite,
		uintptr(unsafe.Pointer(obj)),
		uintptr(unsafe.Pointer(pClientSite)))

	return HRESULT(ret)
}

func (obj *IOleObject) SetHostNames(szContainerApp, szContainerObj *uint16) HRESULT {
	ret, _, _ := syscall.SyscallN(obj.LpVtbl.SetHostNames,
		uintptr(unsafe.Pointer(obj)),
		uintptr(unsafe.Pointer(szContainerApp)),
		uintptr(unsafe.Pointer(szContainerObj)))

	return HRESULT(ret)
}

func (obj *IOleObject) Close(dwSaveOption uint32) HRESULT {
	ret, _, _ := syscall.SyscallN(obj.LpVtbl.Close,
		uintptr(unsafe.Pointer(obj)),
		uintptr(dwSaveOption))

	return HRESULT(ret)
}

func (obj *IOleObject) DoVerb(iVerb int32, lpmsg *MSG, pActiveSite *IOleClientSite, lindex int32, hwndParent HWND, lprcPosRect *RECT) HRESULT {
	ret, _, _ := syscall.SyscallN(obj.LpVtbl.DoVerb,
		uintptr(unsafe.Pointer(obj)),
		uintptr(iVerb),
		uintptr(unsafe.Pointer(lpmsg)),
		uintptr(unsafe.Pointer(pActiveSite)),
		uintptr(lindex),
		uintptr(hwndParent),
		uintptr(unsafe.Pointer(lprcPosRect)))

	return HRESULT(ret)
}

type COAUTHIDENTITY struct {
	User           *uint16
	UserLength     uint32
	Domain         *uint16
	DomainLength   uint32
	Password       *uint16
	PasswordLength uint32
	Flags          uint32
}

type COAUTHINFO struct {
	dwAuthnSvc           uint32
	dwAuthzSvc           uint32
	pwszServerPrincName  *uint16
	dwAuthnLevel         uint32
	dwImpersonationLevel uint32
	pAuthIdentityData    *COAUTHIDENTITY
	dwCapabilities       uint32
}

type COSERVERINFO struct {
	dwReserved1 uint32
	pwszName    *uint16
	pAuthInfo   *COAUTHINFO
	dwReserved2 uint32
}

var (
	oleInitialize *windows.LazyProc
)

func init() {
	// Functions
	oleInitialize = libole32.NewProc("OleInitialize")
}

func OleInitialize() HRESULT {
	ret, _, _ := syscall.SyscallN(oleInitialize.Addr(), 0) // pvReserved

	return HRESULT(ret)
}
