// Copyright 2010 The win Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package win

import (
	"syscall"
	"unsafe"
)

func (wb2 *IWebBrowser2) QueryInterface(riid REFIID, ppvObject *unsafe.Pointer) HRESULT {
	ret, _, _ := syscall.SyscallN(wb2.LpVtbl.QueryInterface,
		uintptr(unsafe.Pointer(wb2)),
		uintptr(unsafe.Pointer(riid)),
		uintptr(unsafe.Pointer(ppvObject)))

	return HRESULT(ret)
}

func (wb2 *IWebBrowser2) Release() HRESULT {
	ret, _, _ := syscall.SyscallN(wb2.LpVtbl.Release,
		uintptr(unsafe.Pointer(wb2)))

	return HRESULT(ret)
}

func (wb2 *IWebBrowser2) Refresh() HRESULT {
	ret, _, _ := syscall.SyscallN(wb2.LpVtbl.Refresh,
		uintptr(unsafe.Pointer(wb2)))

	return HRESULT(ret)
}

func (wb2 *IWebBrowser2) Put_Left(Left int32) HRESULT {
	ret, _, _ := syscall.SyscallN(wb2.LpVtbl.Put_Left,
		uintptr(unsafe.Pointer(wb2)),
		uintptr(Left))

	return HRESULT(ret)
}

func (wb2 *IWebBrowser2) Put_Top(Top int32) HRESULT {
	ret, _, _ := syscall.SyscallN(wb2.LpVtbl.Put_Top,
		uintptr(unsafe.Pointer(wb2)),
		uintptr(Top))

	return HRESULT(ret)
}

func (wb2 *IWebBrowser2) Put_Width(Width int32) HRESULT {
	ret, _, _ := syscall.SyscallN(wb2.LpVtbl.Put_Width,
		uintptr(unsafe.Pointer(wb2)),
		uintptr(Width))

	return HRESULT(ret)
}

func (wb2 *IWebBrowser2) Put_Height(Height int32) HRESULT {
	ret, _, _ := syscall.SyscallN(wb2.LpVtbl.Put_Height,
		uintptr(unsafe.Pointer(wb2)),
		uintptr(Height))

	return HRESULT(ret)
}

func (wb2 *IWebBrowser2) Get_LocationURL(pbstrLocationURL **uint16 /*BSTR*/) HRESULT {
	ret, _, _ := syscall.SyscallN(wb2.LpVtbl.Get_LocationURL,
		uintptr(unsafe.Pointer(wb2)),
		uintptr(unsafe.Pointer(pbstrLocationURL)))

	return HRESULT(ret)
}

func (wb2 *IWebBrowser2) Navigate2(URL *VAR_BSTR, Flags *VAR_I4, TargetFrameName *VAR_BSTR, PostData unsafe.Pointer, Headers *VAR_BSTR) HRESULT {
	ret, _, _ := syscall.SyscallN(wb2.LpVtbl.Navigate2,
		uintptr(unsafe.Pointer(wb2)),
		uintptr(unsafe.Pointer(URL)),
		uintptr(unsafe.Pointer(Flags)),
		uintptr(unsafe.Pointer(TargetFrameName)),
		uintptr(PostData),
		uintptr(unsafe.Pointer(Headers)))

	return HRESULT(ret)
}

func (activeObj *IOleInPlaceActiveObject) Release() HRESULT {
	ret, _, _ := syscall.SyscallN(activeObj.LpVtbl.Release,
		uintptr(unsafe.Pointer(activeObj)))

	return HRESULT(ret)
}

func (activeObj *IOleInPlaceActiveObject) GetWindow(hWndPtr *HWND) HRESULT {
	ret, _, _ := syscall.SyscallN(activeObj.LpVtbl.GetWindow,
		uintptr(unsafe.Pointer(activeObj)),
		uintptr(unsafe.Pointer(hWndPtr)))

	return HRESULT(ret)
}

func (activeObj *IOleInPlaceActiveObject) TranslateAccelerator(msg *MSG) HRESULT {
	ret, _, _ := syscall.SyscallN(activeObj.LpVtbl.TranslateAccelerator,
		uintptr(unsafe.Pointer(activeObj)),
		uintptr(unsafe.Pointer(msg)))

	return HRESULT(ret)
}
