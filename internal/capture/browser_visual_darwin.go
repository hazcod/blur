package capture

import (
	"context"

	"github.com/hazcod/blur/internal/platform/darwin"
)

// Browser pixels require the same retained document identity as AX text, plus
// a fresh bounded chrome/page discovery and exact viewport geometry. All AX
// operations here read metadata only; unknown authority never permits pixels.
func verifyBrowserVisualRegion(ctx context.Context, api nativeAXAPI, pid int32, app string, windowID uintptr, original *browserPageLease, domain string, candidate darwin.ScreenTextWindow, limits Limits) (darwin.ScreenTextRect, error) {
	reject := func(err error) (darwin.ScreenTextRect, error) {
		return darwin.ScreenTextRect{}, captureError(Excluded, err)
	}
	if original == nil || original.root == nil || domain == "" {
		return reject(nil)
	}
	if err := validateBrowserWindow(ctx, api, windowID); err != nil {
		return reject(err)
	}
	if err := verifyVisualAXWindow(ctx, api, pid, app, windowID, candidate, limits); err != nil {
		return darwin.ScreenTextRect{}, err
	}
	if err := original.validate(ctx); err != nil {
		return reject(err)
	}
	window := &nativeAXNode{api: api, element: api.retain(windowID)}
	defer window.release()
	fresh, err := findBrowserPage(ctx, window, limits)
	if err != nil {
		return reject(err)
	}
	defer fresh.release()
	selected, ok := original.root.(*nativeAXNode)
	if !ok || !selected.sameElement(fresh.root) {
		return reject(nil)
	}
	verifyPage := func() error {
		if err := original.validate(ctx); err != nil {
			return err
		}
		if err := fresh.validate(ctx); err != nil {
			return err
		}
		current, known, err := fresh.domain(ctx)
		if err != nil {
			return err
		}
		if !known || current != domain {
			return errCaptureDomainChanged
		}
		nodes := []browserTreeNode{fresh.window, fresh.root}
		for _, edge := range fresh.path {
			nodes = append(nodes, edge.parent)
		}
		for _, node := range nodes {
			role, err := node.role(ctx)
			if err != nil {
				return err
			}
			secret, err := node.sensitive(ctx, role)
			if err != nil {
				return err
			}
			if !validAXRole(role) || secret {
				return captureError(Excluded, nil)
			}
		}
		return ctx.Err()
	}
	if err := verifyPage(); err != nil {
		return reject(err)
	}
	region, err := browserPageViewport(ctx, fresh, candidate.Frame, func(ctx context.Context, node axTreeNode) (darwin.ScreenTextRect, error) {
		native, ok := node.(*nativeAXNode)
		if !ok {
			return darwin.ScreenTextRect{}, captureError(Excluded, nil)
		}
		position, err := native.visualPair(ctx, "AXPosition", 1)
		if err != nil {
			return darwin.ScreenTextRect{}, err
		}
		size, err := native.visualPair(ctx, "AXSize", 2)
		if err != nil {
			return darwin.ScreenTextRect{}, err
		}
		return darwin.ScreenTextRect{X: position.X, Y: position.Y, Width: size.X, Height: size.Y}, nil
	})
	if err != nil {
		return reject(err)
	}
	if err := verifyPage(); err != nil {
		return reject(err)
	}
	if api.secureInput == nil || api.secureInput() {
		return reject(nil)
	}
	if err := validateBrowserWindow(ctx, api, windowID); err != nil {
		return reject(err)
	}
	if err := validateFocusedWindow(ctx, api, pid, windowID); err != nil {
		return reject(err)
	}
	return region, ctx.Err()
}
