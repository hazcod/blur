package capture

import (
	"context"
	"runtime"

	"github.com/hazcod/blur/internal/platform/darwin"
)

const maxVisualAXWindows = 128

// AX has no public focused-window CGWindowID accessor. Title/geometry alone
// are not unique: a non-key twin can be above the focused window. Require the
// focused proxy in a bounded public AXWindows list, with no other proxy sharing
// its geometry. Include hidden/minimized AX windows; an on-screen-only CG list
// cannot establish this uniqueness. Unverifiable lists fail closed.
func verifyVisualAXWindowUnique(ctx context.Context, api nativeAXAPI, pid int32, expected uintptr, frame darwin.ScreenTextRect) error {
	reject := func(err error) error { return captureError(Excluded, err) }
	app := api.createApp(pid)
	if app == 0 {
		return reject(nil)
	}
	defer api.release(app)
	if err := setAXTimeout(ctx, api, app); err != nil {
		return reject(err)
	}
	attribute, storage, err := createAXAttribute(api, "AXWindows")
	if err != nil {
		return reject(err)
	}
	defer api.release(attribute)
	defer runtime.KeepAlive(storage)
	var count int64
	if api.valueCount(app, attribute, &count) != axSuccess || count < 1 || count > maxVisualAXWindows {
		return reject(nil)
	}
	var array uintptr
	status := api.copyValues(app, attribute, 0, count, &array)
	if array != 0 {
		defer api.release(array)
	}
	if status != axSuccess || array == 0 || api.getTypeID(array) != api.arrayType() || api.arrayCount(array) != count {
		return reject(nil)
	}
	found := false
	for i := int64(0); i < count; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		window := api.arrayValue(array, i)
		if window == 0 || api.getTypeID(window) != axElementTypeID() {
			return reject(nil)
		}
		if api.equal(window, expected) {
			if found {
				return reject(nil)
			}
			found = true
			continue
		}
		node := &nativeAXNode{api: api, element: window}
		position, err := node.visualPair(ctx, "AXPosition", 1)
		if err != nil {
			return reject(err)
		}
		size, err := node.visualPair(ctx, "AXSize", 2)
		if err != nil {
			return reject(err)
		}
		if visualGeometryMatches(frame, position, size) {
			return reject(nil)
		}
	}
	var currentCount int64
	if !found || api.valueCount(app, attribute, &currentCount) != axSuccess || currentCount != count {
		return reject(nil)
	}
	return ctx.Err()
}
