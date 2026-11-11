// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package win

import (
	"syscall"
	"unsafe"
)

// Microsoft Edge WebView2.
//
// The WebView2 interfaces are not in the Win32 metadata; they come from the
// WebView2 SDK (NuGet package Microsoft.Web.WebView2). The interface IDs
// and the vtable order below were taken from WebView2.h of SDK version
// 1.0.1150.38 (the copy vendored in github.com/webview/webview_go,
// libs/mswebview2/include). Interfaces of WebView2 never change once
// released; newer SDKs add new interfaces (ICoreWebView2_2, ...) instead,
// so these declarations stay valid with newer runtimes.
//
// Every vtable lists all methods of the interface in vtable order, but only
// the methods walk calls are declared as Go methods. The handler and
// options interfaces are implemented in Go with package internal/com.

// COREWEBVIEW2_MOVE_FOCUS_REASON
const (
	COREWEBVIEW2_MOVE_FOCUS_REASON_PROGRAMMATIC = 0
	COREWEBVIEW2_MOVE_FOCUS_REASON_NEXT         = 1
	COREWEBVIEW2_MOVE_FOCUS_REASON_PREVIOUS     = 2
)

// Runtime types of CreateWebViewEnvironmentWithOptionsInternal in
// EmbeddedBrowserWebView.dll: an installed (Evergreen) runtime or a fixed
// version runtime in a folder chosen by the application.
const (
	WEBVIEW2_RUNTIME_TYPE_INSTALLED       = 0
	WEBVIEW2_RUNTIME_TYPE_REDISTRIBUTABLE = 1
)

// EventRegistrationToken identifies an event handler added with one of the
// Add_ methods.
type EventRegistrationToken struct {
	Value int64
}

var (
	IID_ICoreWebView2Environment                                   = IID{0xb96d755e, 0x0319, 0x4e92, [8]byte{0xa2, 0x96, 0x23, 0x43, 0x6f, 0x46, 0xa1, 0xfc}}
	IID_ICoreWebView2EnvironmentOptions                            = IID{0x2fde08a8, 0x1e9a, 0x4766, [8]byte{0x8c, 0x05, 0x95, 0xa9, 0xce, 0xb9, 0xd1, 0xc5}}
	IID_ICoreWebView2Controller                                    = IID{0x4d00c0d1, 0x9434, 0x4eb6, [8]byte{0x80, 0x78, 0x86, 0x97, 0xa5, 0x60, 0x33, 0x4f}}
	IID_ICoreWebView2                                              = IID{0x76eceacb, 0x0462, 0x4d94, [8]byte{0xac, 0x83, 0x42, 0x3a, 0x67, 0x93, 0x77, 0x5e}}
	IID_ICoreWebView2NavigationStartingEventArgs                   = IID{0x5b495469, 0xe119, 0x438a, [8]byte{0x9b, 0x18, 0x76, 0x04, 0xf2, 0x5f, 0x2e, 0x49}}
	IID_ICoreWebView2NavigationCompletedEventArgs                  = IID{0x30d68b7d, 0x20d9, 0x4752, [8]byte{0xa9, 0xca, 0xec, 0x84, 0x48, 0xfb, 0xb5, 0xc1}}
	IID_ICoreWebView2WebMessageReceivedEventArgs                   = IID{0x0f99a40c, 0xe962, 0x4207, [8]byte{0x9e, 0x92, 0xe3, 0xd5, 0x42, 0xef, 0xf8, 0x49}}
	IID_ICoreWebView2MoveFocusRequestedEventArgs                   = IID{0x2d6aa13b, 0x3839, 0x4a15, [8]byte{0x92, 0xfc, 0xd8, 0x8b, 0x3c, 0x0d, 0x9c, 0x9d}}
	IID_ICoreWebView2CreateCoreWebView2EnvironmentCompletedHandler = IID{0x4e8a3389, 0xc9d8, 0x4bd2, [8]byte{0xb6, 0xb5, 0x12, 0x4f, 0xee, 0x6c, 0xc1, 0x4d}}
	IID_ICoreWebView2CreateCoreWebView2ControllerCompletedHandler  = IID{0x6c4819f3, 0xc9b7, 0x4260, [8]byte{0x81, 0x27, 0xc9, 0xf5, 0xbd, 0xe7, 0xf6, 0x8c}}
	IID_ICoreWebView2NavigationStartingEventHandler                = IID{0x9adbe429, 0xf36d, 0x432b, [8]byte{0x9d, 0xdc, 0xf8, 0x88, 0x1f, 0xbd, 0x76, 0xe3}}
	IID_ICoreWebView2NavigationCompletedEventHandler               = IID{0xd33a35bf, 0x1c49, 0x4f98, [8]byte{0x93, 0xab, 0x00, 0x6e, 0x05, 0x33, 0xfe, 0x1c}}
	IID_ICoreWebView2SourceChangedEventHandler                     = IID{0x3c067f9f, 0x5388, 0x4772, [8]byte{0x8b, 0x48, 0x79, 0xf7, 0xef, 0x1a, 0xb3, 0x7c}}
	IID_ICoreWebView2HistoryChangedEventHandler                    = IID{0xc79a420c, 0xefd9, 0x4058, [8]byte{0x92, 0x95, 0x3e, 0x8b, 0x4b, 0xca, 0xb6, 0x45}}
	IID_ICoreWebView2DocumentTitleChangedEventHandler              = IID{0xf5f2b923, 0x953e, 0x4042, [8]byte{0x9f, 0x95, 0xf3, 0xa1, 0x18, 0xe1, 0xaf, 0xd4}}
	IID_ICoreWebView2WebMessageReceivedEventHandler                = IID{0x57213f19, 0x00e6, 0x49fa, [8]byte{0x8e, 0x07, 0x89, 0x8e, 0xa0, 0x1e, 0xcb, 0xd2}}
	IID_ICoreWebView2ExecuteScriptCompletedHandler                 = IID{0x49511172, 0xcc67, 0x4bca, [8]byte{0x99, 0x23, 0x13, 0x71, 0x12, 0xf4, 0xc4, 0xcc}}
	IID_ICoreWebView2MoveFocusRequestedEventHandler                = IID{0x69035451, 0x6dc7, 0x4cb8, [8]byte{0x9b, 0xce, 0xb2, 0xbd, 0x70, 0xad, 0x28, 0x9f}}
)

type ICoreWebView2EnvironmentVtbl struct {
	QueryInterface                    uintptr
	AddRef                            uintptr
	Release                           uintptr
	CreateCoreWebView2Controller      uintptr
	CreateWebResourceResponse         uintptr
	Get_BrowserVersionString          uintptr
	Add_NewBrowserVersionAvailable    uintptr
	Remove_NewBrowserVersionAvailable uintptr
}

type ICoreWebView2Environment struct {
	LpVtbl *ICoreWebView2EnvironmentVtbl
}

type ICoreWebView2EnvironmentOptionsVtbl struct {
	QueryInterface                             uintptr
	AddRef                                     uintptr
	Release                                    uintptr
	Get_AdditionalBrowserArguments             uintptr
	Put_AdditionalBrowserArguments             uintptr
	Get_Language                               uintptr
	Put_Language                               uintptr
	Get_TargetCompatibleBrowserVersion         uintptr
	Put_TargetCompatibleBrowserVersion         uintptr
	Get_AllowSingleSignOnUsingOSPrimaryAccount uintptr
	Put_AllowSingleSignOnUsingOSPrimaryAccount uintptr
}

type ICoreWebView2EnvironmentOptions struct {
	LpVtbl *ICoreWebView2EnvironmentOptionsVtbl
}

type ICoreWebView2ControllerVtbl struct {
	QueryInterface                    uintptr
	AddRef                            uintptr
	Release                           uintptr
	Get_IsVisible                     uintptr
	Put_IsVisible                     uintptr
	Get_Bounds                        uintptr
	Put_Bounds                        uintptr
	Get_ZoomFactor                    uintptr
	Put_ZoomFactor                    uintptr
	Add_ZoomFactorChanged             uintptr
	Remove_ZoomFactorChanged          uintptr
	SetBoundsAndZoomFactor            uintptr
	MoveFocus                         uintptr
	Add_MoveFocusRequested            uintptr
	Remove_MoveFocusRequested         uintptr
	Add_GotFocus                      uintptr
	Remove_GotFocus                   uintptr
	Add_LostFocus                     uintptr
	Remove_LostFocus                  uintptr
	Add_AcceleratorKeyPressed         uintptr
	Remove_AcceleratorKeyPressed      uintptr
	Get_ParentWindow                  uintptr
	Put_ParentWindow                  uintptr
	NotifyParentWindowPositionChanged uintptr
	Close                             uintptr
	Get_CoreWebView2                  uintptr
}

type ICoreWebView2Controller struct {
	LpVtbl *ICoreWebView2ControllerVtbl
}

type ICoreWebView2Vtbl struct {
	QueryInterface                          uintptr
	AddRef                                  uintptr
	Release                                 uintptr
	Get_Settings                            uintptr
	Get_Source                              uintptr
	Navigate                                uintptr
	NavigateToString                        uintptr
	Add_NavigationStarting                  uintptr
	Remove_NavigationStarting               uintptr
	Add_ContentLoading                      uintptr
	Remove_ContentLoading                   uintptr
	Add_SourceChanged                       uintptr
	Remove_SourceChanged                    uintptr
	Add_HistoryChanged                      uintptr
	Remove_HistoryChanged                   uintptr
	Add_NavigationCompleted                 uintptr
	Remove_NavigationCompleted              uintptr
	Add_FrameNavigationStarting             uintptr
	Remove_FrameNavigationStarting          uintptr
	Add_FrameNavigationCompleted            uintptr
	Remove_FrameNavigationCompleted         uintptr
	Add_ScriptDialogOpening                 uintptr
	Remove_ScriptDialogOpening              uintptr
	Add_PermissionRequested                 uintptr
	Remove_PermissionRequested              uintptr
	Add_ProcessFailed                       uintptr
	Remove_ProcessFailed                    uintptr
	AddScriptToExecuteOnDocumentCreated     uintptr
	RemoveScriptToExecuteOnDocumentCreated  uintptr
	ExecuteScript                           uintptr
	CapturePreview                          uintptr
	Reload                                  uintptr
	PostWebMessageAsJson                    uintptr
	PostWebMessageAsString                  uintptr
	Add_WebMessageReceived                  uintptr
	Remove_WebMessageReceived               uintptr
	CallDevToolsProtocolMethod              uintptr
	Get_BrowserProcessId                    uintptr
	Get_CanGoBack                           uintptr
	Get_CanGoForward                        uintptr
	GoBack                                  uintptr
	GoForward                               uintptr
	GetDevToolsProtocolEventReceiver        uintptr
	Stop                                    uintptr
	Add_NewWindowRequested                  uintptr
	Remove_NewWindowRequested               uintptr
	Add_DocumentTitleChanged                uintptr
	Remove_DocumentTitleChanged             uintptr
	Get_DocumentTitle                       uintptr
	AddHostObjectToScript                   uintptr
	RemoveHostObjectFromScript              uintptr
	OpenDevToolsWindow                      uintptr
	Add_ContainsFullScreenElementChanged    uintptr
	Remove_ContainsFullScreenElementChanged uintptr
	Get_ContainsFullScreenElement           uintptr
	Add_WebResourceRequested                uintptr
	Remove_WebResourceRequested             uintptr
	AddWebResourceRequestedFilter           uintptr
	RemoveWebResourceRequestedFilter        uintptr
	Add_WindowCloseRequested                uintptr
	Remove_WindowCloseRequested             uintptr
}

type ICoreWebView2 struct {
	LpVtbl *ICoreWebView2Vtbl
}

type ICoreWebView2NavigationStartingEventArgsVtbl struct {
	QueryInterface      uintptr
	AddRef              uintptr
	Release             uintptr
	Get_Uri             uintptr
	Get_IsUserInitiated uintptr
	Get_IsRedirected    uintptr
	Get_RequestHeaders  uintptr
	Get_Cancel          uintptr
	Put_Cancel          uintptr
	Get_NavigationId    uintptr
}

type ICoreWebView2NavigationStartingEventArgs struct {
	LpVtbl *ICoreWebView2NavigationStartingEventArgsVtbl
}

type ICoreWebView2NavigationCompletedEventArgsVtbl struct {
	QueryInterface     uintptr
	AddRef             uintptr
	Release            uintptr
	Get_IsSuccess      uintptr
	Get_WebErrorStatus uintptr
	Get_NavigationId   uintptr
}

type ICoreWebView2NavigationCompletedEventArgs struct {
	LpVtbl *ICoreWebView2NavigationCompletedEventArgsVtbl
}

type ICoreWebView2WebMessageReceivedEventArgsVtbl struct {
	QueryInterface           uintptr
	AddRef                   uintptr
	Release                  uintptr
	Get_Source               uintptr
	Get_WebMessageAsJson     uintptr
	TryGetWebMessageAsString uintptr
}

type ICoreWebView2WebMessageReceivedEventArgs struct {
	LpVtbl *ICoreWebView2WebMessageReceivedEventArgsVtbl
}

type ICoreWebView2MoveFocusRequestedEventArgsVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	Get_Reason     uintptr
	Get_Handled    uintptr
	Put_Handled    uintptr
}

type ICoreWebView2MoveFocusRequestedEventArgs struct {
	LpVtbl *ICoreWebView2MoveFocusRequestedEventArgsVtbl
}

type ICoreWebView2CreateCoreWebView2EnvironmentCompletedHandlerVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	Invoke         uintptr
}

type ICoreWebView2CreateCoreWebView2EnvironmentCompletedHandler struct {
	LpVtbl *ICoreWebView2CreateCoreWebView2EnvironmentCompletedHandlerVtbl
}

type ICoreWebView2CreateCoreWebView2ControllerCompletedHandlerVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	Invoke         uintptr
}

type ICoreWebView2CreateCoreWebView2ControllerCompletedHandler struct {
	LpVtbl *ICoreWebView2CreateCoreWebView2ControllerCompletedHandlerVtbl
}

type ICoreWebView2NavigationStartingEventHandlerVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	Invoke         uintptr
}

type ICoreWebView2NavigationStartingEventHandler struct {
	LpVtbl *ICoreWebView2NavigationStartingEventHandlerVtbl
}

type ICoreWebView2NavigationCompletedEventHandlerVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	Invoke         uintptr
}

type ICoreWebView2NavigationCompletedEventHandler struct {
	LpVtbl *ICoreWebView2NavigationCompletedEventHandlerVtbl
}

type ICoreWebView2SourceChangedEventHandlerVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	Invoke         uintptr
}

type ICoreWebView2SourceChangedEventHandler struct {
	LpVtbl *ICoreWebView2SourceChangedEventHandlerVtbl
}

type ICoreWebView2HistoryChangedEventHandlerVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	Invoke         uintptr
}

type ICoreWebView2HistoryChangedEventHandler struct {
	LpVtbl *ICoreWebView2HistoryChangedEventHandlerVtbl
}

type ICoreWebView2DocumentTitleChangedEventHandlerVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	Invoke         uintptr
}

type ICoreWebView2DocumentTitleChangedEventHandler struct {
	LpVtbl *ICoreWebView2DocumentTitleChangedEventHandlerVtbl
}

type ICoreWebView2WebMessageReceivedEventHandlerVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	Invoke         uintptr
}

type ICoreWebView2WebMessageReceivedEventHandler struct {
	LpVtbl *ICoreWebView2WebMessageReceivedEventHandlerVtbl
}

type ICoreWebView2ExecuteScriptCompletedHandlerVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	Invoke         uintptr
}

type ICoreWebView2ExecuteScriptCompletedHandler struct {
	LpVtbl *ICoreWebView2ExecuteScriptCompletedHandlerVtbl
}

type ICoreWebView2MoveFocusRequestedEventHandlerVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	Invoke         uintptr
}

type ICoreWebView2MoveFocusRequestedEventHandler struct {
	LpVtbl *ICoreWebView2MoveFocusRequestedEventHandlerVtbl
}

// ICoreWebView2Environment

func (obj *ICoreWebView2Environment) QueryInterface(riid REFIID, ppvObject *unsafe.Pointer) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.QueryInterface, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(riid)), uintptr(unsafe.Pointer(ppvObject)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2Environment) Release() uint32 {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Release, uintptr(unsafe.Pointer(obj)))
	return uint32(r1)
}

func (obj *ICoreWebView2Environment) CreateCoreWebView2Controller(parentWindow HWND, handler *ICoreWebView2CreateCoreWebView2ControllerCompletedHandler) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.CreateCoreWebView2Controller, uintptr(unsafe.Pointer(obj)), uintptr(parentWindow), uintptr(unsafe.Pointer(handler)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2Environment) Get_BrowserVersionString(versionInfo **uint16) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Get_BrowserVersionString, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(versionInfo)))
	return HRESULT(r1)
}

// ICoreWebView2Controller (Put_Bounds is in the architecture files)

func (obj *ICoreWebView2Controller) AddRef() uint32 {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.AddRef, uintptr(unsafe.Pointer(obj)))
	return uint32(r1)
}

func (obj *ICoreWebView2Controller) Release() uint32 {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Release, uintptr(unsafe.Pointer(obj)))
	return uint32(r1)
}

func (obj *ICoreWebView2Controller) Get_IsVisible(isVisible *BOOL) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Get_IsVisible, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(isVisible)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2Controller) Put_IsVisible(isVisible BOOL) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Put_IsVisible, uintptr(unsafe.Pointer(obj)), uintptr(isVisible))
	return HRESULT(r1)
}

func (obj *ICoreWebView2Controller) Get_Bounds(bounds *RECT) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Get_Bounds, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(bounds)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2Controller) MoveFocus(reason int32) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.MoveFocus, uintptr(unsafe.Pointer(obj)), uintptr(reason))
	return HRESULT(r1)
}

func (obj *ICoreWebView2Controller) Add_MoveFocusRequested(eventHandler *ICoreWebView2MoveFocusRequestedEventHandler, token *EventRegistrationToken) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Add_MoveFocusRequested, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(eventHandler)), uintptr(unsafe.Pointer(token)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2Controller) Remove_MoveFocusRequested(token EventRegistrationToken) HRESULT {
	return removeEventHandler(obj.LpVtbl.Remove_MoveFocusRequested, unsafe.Pointer(obj), token)
}

func (obj *ICoreWebView2Controller) NotifyParentWindowPositionChanged() HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.NotifyParentWindowPositionChanged, uintptr(unsafe.Pointer(obj)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2Controller) Close() HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Close, uintptr(unsafe.Pointer(obj)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2Controller) Get_CoreWebView2(coreWebView2 **ICoreWebView2) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Get_CoreWebView2, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(coreWebView2)))
	return HRESULT(r1)
}

// ICoreWebView2

func (obj *ICoreWebView2) Release() uint32 {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Release, uintptr(unsafe.Pointer(obj)))
	return uint32(r1)
}

func (obj *ICoreWebView2) Get_Source(uri **uint16) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Get_Source, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(uri)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2) Navigate(uri *uint16) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Navigate, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(uri)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2) NavigateToString(htmlContent *uint16) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.NavigateToString, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(htmlContent)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2) Add_NavigationStarting(eventHandler *ICoreWebView2NavigationStartingEventHandler, token *EventRegistrationToken) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Add_NavigationStarting, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(eventHandler)), uintptr(unsafe.Pointer(token)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2) Add_SourceChanged(eventHandler *ICoreWebView2SourceChangedEventHandler, token *EventRegistrationToken) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Add_SourceChanged, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(eventHandler)), uintptr(unsafe.Pointer(token)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2) Add_HistoryChanged(eventHandler *ICoreWebView2HistoryChangedEventHandler, token *EventRegistrationToken) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Add_HistoryChanged, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(eventHandler)), uintptr(unsafe.Pointer(token)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2) Add_NavigationCompleted(eventHandler *ICoreWebView2NavigationCompletedEventHandler, token *EventRegistrationToken) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Add_NavigationCompleted, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(eventHandler)), uintptr(unsafe.Pointer(token)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2) ExecuteScript(javaScript *uint16, handler *ICoreWebView2ExecuteScriptCompletedHandler) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.ExecuteScript, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(javaScript)), uintptr(unsafe.Pointer(handler)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2) Reload() HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Reload, uintptr(unsafe.Pointer(obj)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2) PostWebMessageAsJson(webMessageAsJson *uint16) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.PostWebMessageAsJson, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(webMessageAsJson)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2) PostWebMessageAsString(webMessageAsString *uint16) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.PostWebMessageAsString, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(webMessageAsString)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2) Add_WebMessageReceived(handler *ICoreWebView2WebMessageReceivedEventHandler, token *EventRegistrationToken) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Add_WebMessageReceived, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(handler)), uintptr(unsafe.Pointer(token)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2) Get_CanGoBack(canGoBack *BOOL) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Get_CanGoBack, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(canGoBack)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2) Get_CanGoForward(canGoForward *BOOL) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Get_CanGoForward, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(canGoForward)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2) GoBack() HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.GoBack, uintptr(unsafe.Pointer(obj)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2) GoForward() HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.GoForward, uintptr(unsafe.Pointer(obj)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2) Stop() HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Stop, uintptr(unsafe.Pointer(obj)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2) Add_DocumentTitleChanged(eventHandler *ICoreWebView2DocumentTitleChangedEventHandler, token *EventRegistrationToken) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Add_DocumentTitleChanged, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(eventHandler)), uintptr(unsafe.Pointer(token)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2) Get_DocumentTitle(title **uint16) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Get_DocumentTitle, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(title)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2) Remove_NavigationStarting(token EventRegistrationToken) HRESULT {
	return removeEventHandler(obj.LpVtbl.Remove_NavigationStarting, unsafe.Pointer(obj), token)
}

func (obj *ICoreWebView2) Remove_SourceChanged(token EventRegistrationToken) HRESULT {
	return removeEventHandler(obj.LpVtbl.Remove_SourceChanged, unsafe.Pointer(obj), token)
}

func (obj *ICoreWebView2) Remove_HistoryChanged(token EventRegistrationToken) HRESULT {
	return removeEventHandler(obj.LpVtbl.Remove_HistoryChanged, unsafe.Pointer(obj), token)
}

func (obj *ICoreWebView2) Remove_NavigationCompleted(token EventRegistrationToken) HRESULT {
	return removeEventHandler(obj.LpVtbl.Remove_NavigationCompleted, unsafe.Pointer(obj), token)
}

func (obj *ICoreWebView2) Remove_WebMessageReceived(token EventRegistrationToken) HRESULT {
	return removeEventHandler(obj.LpVtbl.Remove_WebMessageReceived, unsafe.Pointer(obj), token)
}

func (obj *ICoreWebView2) Remove_DocumentTitleChanged(token EventRegistrationToken) HRESULT {
	return removeEventHandler(obj.LpVtbl.Remove_DocumentTitleChanged, unsafe.Pointer(obj), token)
}

// Event arguments

func (obj *ICoreWebView2NavigationStartingEventArgs) Get_Uri(uri **uint16) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Get_Uri, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(uri)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2NavigationCompletedEventArgs) Get_IsSuccess(isSuccess *BOOL) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Get_IsSuccess, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(isSuccess)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2NavigationCompletedEventArgs) Get_WebErrorStatus(webErrorStatus *int32) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Get_WebErrorStatus, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(webErrorStatus)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2WebMessageReceivedEventArgs) Get_Source(source **uint16) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Get_Source, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(source)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2WebMessageReceivedEventArgs) Get_WebMessageAsJson(webMessageAsJson **uint16) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Get_WebMessageAsJson, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(webMessageAsJson)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2WebMessageReceivedEventArgs) TryGetWebMessageAsString(webMessageAsString **uint16) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.TryGetWebMessageAsString, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(webMessageAsString)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2MoveFocusRequestedEventArgs) Get_Reason(reason *int32) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Get_Reason, uintptr(unsafe.Pointer(obj)), uintptr(unsafe.Pointer(reason)))
	return HRESULT(r1)
}

func (obj *ICoreWebView2MoveFocusRequestedEventArgs) Put_Handled(value BOOL) HRESULT {
	r1, _, _ := syscall.SyscallN(obj.LpVtbl.Put_Handled, uintptr(unsafe.Pointer(obj)), uintptr(value))
	return HRESULT(r1)
}

// CoTaskMemString returns the string s points to and frees s with
// CoTaskMemFree. WebView2 returns strings allocated with CoTaskMemAlloc.
func CoTaskMemString(s *uint16) string {
	if s == nil {
		return ""
	}
	str := UTF16PtrToString(s)
	CoTaskMemFree(uintptr(unsafe.Pointer(s)))
	return str
}
