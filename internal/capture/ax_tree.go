package capture

import (
	"context"
	"errors"
	"sync"
)

var errAXValueTruncated = errors.New("capture: accessibility value truncated")

// axTreeNode exposes role and bounded structural access separately from text.
// Implementations own a native reference until release; value is called only
// after the capture pipeline has validated the bounded structure and profile.
type axTreeNode interface {
	identity() uintptr
	role(context.Context) (string, error)
	sensitive(context.Context, string) (bool, error)
	children(context.Context, int) ([]axTreeNode, bool, error)
	value(context.Context, int) (string, error)
	release()
}

func collectAXFrame(ctx context.Context, root axTreeNode, limits Limits) (rawFrame, error) {
	if root == nil || !limits.valid() {
		return rawFrame{}, errors.New("capture: accessibility root unavailable")
	}
	owned := make([]axTreeNode, 0, limits.MaxNodes)
	seen := make(map[uintptr]struct{}, limits.MaxNodes)
	cleanup := func() {
		for i := len(owned) - 1; i >= 0; i-- {
			owned[i].release()
		}
	}
	add := func(node axTreeNode) bool {
		if node == nil {
			return false
		}
		identity := node.identity()
		if identity == 0 {
			node.release()
			return false
		}
		if _, ok := seen[identity]; ok {
			node.release()
			return false
		}
		seen[identity] = struct{}{}
		owned = append(owned, node)
		return true
	}
	if !add(root) {
		return rawFrame{}, errors.New("capture: invalid accessibility root")
	}
	truncated := false
	var visit func(axTreeNode, int) (*rawNode, error)
	visit = func(node axTreeNode, depth int) (*rawNode, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		role, err := node.role(ctx)
		if err != nil || role != "" && !validAXRole(role) {
			return nil, errors.New("capture: accessibility role unavailable")
		}
		sensitive, err := node.sensitive(ctx, role)
		if err != nil {
			return nil, errors.New("capture: accessibility sensitivity unavailable")
		}
		current := &rawNode{
			role: role, sensitive: sensitive, declaredBytes: -1, boundedValue: true,
			value: node.value,
		}
		if sensitive {
			current.value = nil
			return current, nil
		}
		childLimit := limits.MaxNodes - len(owned) + 1
		if depth >= limits.MaxDepth {
			children, more, childErr := node.children(ctx, 1)
			if childErr != nil {
				return nil, errors.New("capture: accessibility children unavailable")
			}
			for _, child := range children {
				child.release()
			}
			if more || len(children) != 0 {
				truncated = true
			}
			return current, nil
		}
		if childLimit < 1 {
			truncated = true
			return current, nil
		}
		children, more, err := node.children(ctx, childLimit)
		if err != nil || len(children) > childLimit {
			for _, child := range children {
				child.release()
			}
			return nil, errors.New("capture: accessibility children unavailable")
		}
		remaining := limits.MaxNodes - len(owned)
		if more || len(children) > remaining {
			truncated = true
		}
		if len(children) > remaining {
			for _, child := range children[remaining:] {
				child.release()
			}
			children = children[:remaining]
		}
		current.children = make([]*rawNode, 0, len(children))
		for index, child := range children {
			if err := ctx.Err(); err != nil {
				releaseAXNodes(children[index:])
				return nil, err
			}
			if !add(child) {
				continue
			}
			converted, err := visit(child, depth+1)
			if err != nil {
				releaseAXNodes(children[index+1:])
				return nil, err
			}
			current.children = append(current.children, converted)
		}
		return current, nil
	}
	frameRoot, err := visit(root, 1)
	if err != nil {
		cleanup()
		return rawFrame{}, err
	}
	var releaseOnce sync.Once
	return rawFrame{
		roots: []*rawNode{frameRoot}, truncated: truncated, nativeRootID: root.identity(),
		cleanup: func() { releaseOnce.Do(cleanup) },
	}, nil
}

func releaseAXNodes(nodes []axTreeNode) {
	for _, node := range nodes {
		if node != nil {
			node.release()
		}
	}
}

func wrapAXValues(nodes []*rawNode, wrap func(context.Context, int, func(context.Context, int) (string, error)) (string, error)) {
	for _, node := range nodes {
		if node.value != nil && !node.sensitive {
			original := node.value
			node.value = func(ctx context.Context, max int) (string, error) { return wrap(ctx, max, original) }
		}
		wrapAXValues(node.children, wrap)
	}
}
