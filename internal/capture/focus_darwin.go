//go:build darwin

package capture

import (
	"context"
	"errors"
	"runtime"

	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/darwin"
)

// nativeFocusNode adapts one copied AX element to the breadcrumb reader. It
// owns its reference. Only AXRole, AXSubrole, AXTitle and AXDescription are
// reachable; AXValue and every other attribute are refused here so a future
// edit of the walk cannot start reading what the user typed.
type nativeFocusNode struct{ node *nativeAXNode }

var errFocusAttributeRefused = errors.New("capture: focus attribute not allowed")

func (n nativeFocusNode) role(ctx context.Context) (string, error) { return n.node.role(ctx) }

func (n nativeFocusNode) attribute(ctx context.Context, name string, max int) (string, error) {
	switch name {
	case "AXSubrole", "AXTitle", "AXDescription":
		return n.node.stringAttribute(ctx, name, max)
	default:
		return "", errFocusAttributeRefused
	}
}

func (n nativeFocusNode) parent(ctx context.Context) (focusNode, error) {
	value, status, err := n.node.copyAttribute(ctx, "AXParent")
	if err != nil {
		return nil, err
	}
	if status == axAttributeUnsupported || status == axNoValue || value == 0 {
		return nil, nil
	}
	if n.node.api.getTypeID(value) != axElementTypeID() {
		n.node.api.release(value)
		return nil, NativeReadError(ReadStageRole, 0)
	}
	return nativeFocusNode{node: &nativeAXNode{api: n.node.api, element: value}}, nil
}

func (n nativeFocusNode) identity() uintptr { return n.node.element }
func (n nativeFocusNode) release()          { n.node.release() }

// readNativeFocus reads the keyboard-focused element of pid and accepts it
// only if it sits inside window, the window the capture text came from.
func readNativeFocus(ctx context.Context, api nativeAXAPI, pid int32, window uintptr) (rawFocus, bool) {
	if window == 0 || api.createApp == nil || api.copyValue == nil || api.equal == nil {
		return rawFocus{}, false
	}
	app := api.createApp(pid)
	if app == 0 {
		return rawFocus{}, false
	}
	defer api.release(app)
	if setAXTimeout(ctx, api, app) != nil {
		return rawFocus{}, false
	}
	attribute, storage, err := createAXAttribute(api, "AXFocusedUIElement")
	if err != nil {
		return rawFocus{}, false
	}
	defer api.release(attribute)
	defer runtime.KeepAlive(storage)
	var element uintptr
	status := api.copyValue(app, attribute, &element)
	if status != axSuccess || element == 0 {
		if element != 0 {
			api.release(element)
		}
		return rawFocus{}, false
	}
	if api.getTypeID(element) != axElementTypeID() {
		api.release(element)
		return rawFocus{}, false
	}
	start := nativeFocusNode{node: &nativeAXNode{api: api, element: element}}
	return readFocusBreadcrumb(ctx, start, func(candidate focusNode) bool {
		return api.equal(candidate.identity(), window)
	})
}

// darwinFocusReader returns the frame's best-effort breadcrumb reader. The
// verified-target wrapper keeps it bound to the process the probe selected.
//
// Live behavior (real AXFocusedUIElement/AXParent traversal against running
// apps) cannot be exercised in unit tests and is unverified until native
// acceptance on a signed build.
func darwinFocusReader(source *darwin.Adapter, probe platform.CaptureProbe, window uintptr) func(context.Context) (rawFocus, bool) {
	if source == nil || window == 0 {
		return nil
	}
	return func(ctx context.Context) (rawFocus, bool) {
		var result rawFocus
		var ok bool
		err := source.WithVerifiedTarget(ctx, probe.TargetID, func(ctx context.Context, pid int32) error {
			result, ok = readNativeFocus(ctx, nativeAX.api, pid, window)
			return nil
		})
		if err != nil {
			return rawFocus{}, false
		}
		return result, ok
	}
}
