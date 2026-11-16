package capture

import (
	"context"
	"math"

	"github.com/hazcod/blur/internal/platform/darwin"
)

func finiteVisualRect(r darwin.ScreenTextRect) bool {
	return !math.IsNaN(r.X) && !math.IsNaN(r.Y) && !math.IsNaN(r.Width) && !math.IsNaN(r.Height) &&
		!math.IsInf(r.X, 0) && !math.IsInf(r.Y, 0) && !math.IsInf(r.Width, 0) && !math.IsInf(r.Height, 0) &&
		r.Width >= 1 && r.Height >= 1 && r.Width <= 32768 && r.Height <= 32768 && math.Abs(r.X) <= 1000000 && math.Abs(r.Y) <= 1000000
}

func validVisualRegion(region, window darwin.ScreenTextRect) bool {
	return finiteVisualRect(region) && finiteVisualRect(window) && region != window && region.X >= window.X && region.Y >= window.Y &&
		region.X+region.Width <= window.X+window.Width && region.Y+region.Height <= window.Y+window.Height
}

func intersectVisualRect(a, b darwin.ScreenTextRect) (darwin.ScreenTextRect, bool) {
	if !finiteVisualRect(a) || !finiteVisualRect(b) {
		return darwin.ScreenTextRect{}, false
	}
	x, y := max(a.X, b.X), max(a.Y, b.Y)
	r := darwin.ScreenTextRect{X: x, Y: y, Width: min(a.X+a.Width, b.X+b.Width) - x, Height: min(a.Y+a.Height, b.Y+b.Height) - y}
	return r, finiteVisualRect(r)
}

// Every path ancestor bounds the usable viewport. Unknown geometry rejects;
// visible siblings may not overlap it, including browser chrome and overlays.
func browserPageViewport(ctx context.Context, page *browserPageLease, window darwin.ScreenTextRect, geometry func(context.Context, axTreeNode) (darwin.ScreenTextRect, error)) (darwin.ScreenTextRect, error) {
	reject := func() (darwin.ScreenTextRect, error) { return darwin.ScreenTextRect{}, captureError(Excluded, nil) }
	if page == nil || page.root == nil || geometry == nil {
		return reject()
	}
	region, err := geometry(ctx, page.root)
	if err != nil {
		return darwin.ScreenTextRect{}, err
	}
	var ok bool
	if region, ok = intersectVisualRect(region, window); !ok {
		return reject()
	}
	path := map[uintptr]bool{page.window.identity(): true, page.root.identity(): true}
	for _, edge := range page.path {
		if err := ctx.Err(); err != nil {
			return darwin.ScreenTextRect{}, err
		}
		bounds, err := geometry(ctx, edge.parent)
		if err != nil {
			return darwin.ScreenTextRect{}, err
		}
		if region, ok = intersectVisualRect(region, bounds); !ok {
			return reject()
		}
		path[edge.parent.identity()], path[edge.child] = true, true
	}
	if !validVisualRegion(region, window) {
		return reject()
	}
	if err := browserChromeOutsideViewport(ctx, page, path, region, geometry); err != nil {
		return darwin.ScreenTextRect{}, err
	}
	return region, ctx.Err()
}

// Ordinary page discovery deliberately prunes browser chrome. Pixel authority
// cannot reuse that pruned list: an AX child can extend outside its parent's
// frame. Walk every non-document branch with independent finite bounds, even
// beneath hidden parents, and prove every visible descendant outside the crop.
func browserChromeOutsideViewport(ctx context.Context, page *browserPageLease, path map[uintptr]bool, region darwin.ScreenTextRect, geometry func(context.Context, axTreeNode) (darwin.ScreenTextRect, error)) error {
	type entry struct {
		node  browserTreeNode
		depth int
	}
	queue := []entry{{node: page.window, depth: 1}}
	seen := map[uintptr]bool{page.window.identity(): true}
	var owned []axTreeNode
	defer func() { releaseAXNodes(owned) }()
	reject := func() error { return captureError(Excluded, nil) }
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		current := queue[0]
		queue = queue[1:]
		if current.node.identity() == page.root.identity() {
			continue
		}
		role, err := current.node.role(ctx)
		if err != nil || !validAXRole(role) {
			return reject()
		}
		secret, err := current.node.sensitive(ctx, role)
		if err != nil || secret {
			return reject()
		}
		hidden, err := current.node.hidden(ctx)
		if err != nil {
			return err
		}
		if !path[current.node.identity()] && !hidden {
			bounds, err := geometry(ctx, current.node)
			if err != nil {
				return err
			}
			if !finiteVisualRect(bounds) {
				return reject()
			}
			// A fractional-point overlap can still contain a full Retina pixel.
			if min(region.X+region.Width, bounds.X+bounds.Width) > max(region.X, bounds.X) &&
				min(region.Y+region.Height, bounds.Y+bounds.Height) > max(region.Y, bounds.Y) {
				return reject()
			}
		}
		remaining := browserMetadataNodes - len(seen)
		children, more, err := current.node.children(ctx, max(1, remaining))
		owned = append(owned, children...)
		if err != nil || more || len(children) > remaining || current.depth >= browserMetadataDepth && len(children) > 0 {
			return reject()
		}
		for _, child := range children {
			if child == nil || child.identity() == 0 || seen[child.identity()] {
				return reject()
			}
			node, ok := child.(browserTreeNode)
			if !ok {
				return reject()
			}
			seen[child.identity()] = true
			queue = append(queue, entry{node: node, depth: current.depth + 1})
		}
	}
	return ctx.Err()
}
