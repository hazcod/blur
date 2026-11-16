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

// Optional positive metadata can prove that an otherwise ordinary container has
// no text accessor. Unknown capability keeps the existing deferred reader.
type axValueSupport interface {
	supportsValue(context.Context, string) (bool, error)
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
	var visit func(axTreeNode, int, []axTreeNode) (*rawNode, error)
	visit = func(node axTreeNode, depth int, ancestors []axTreeNode) (*rawNode, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		skipUnavailable := func(message string, cause error) (*rawNode, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if errors.Is(cause, context.Canceled) {
				return nil, context.Canceled
			}
			if errors.Is(cause, context.DeadlineExceeded) {
				return nil, context.DeadlineExceeded
			}
			if depth == 1 {
				if issue := ReadIssueForError(cause); issue.Valid() {
					return nil, NativeReadError(issue.Stage, issue.Status)
				}
				return nil, errors.New(message)
			}
			// One destroyed/custom AX element must not erase valid siblings.
			// Unverified branches stay opaque and make coverage explicitly partial.
			truncated = true
			return &rawNode{role: "AXGroup", sensitive: true, declaredBytes: -1, boundedValue: true}, nil
		}
		role, err := node.role(ctx)
		if err != nil || role != "" && !validAXRole(role) {
			return skipUnavailable("capture: accessibility role unavailable", err)
		}
		sensitive, err := node.sensitive(ctx, role)
		if err != nil {
			return skipUnavailable("capture: accessibility sensitivity unavailable", err)
		}
		current := &rawNode{
			role: role, sensitive: sensitive, declaredBytes: -1, boundedValue: true,
			value: guardAXAncestorValues(node.value, ancestors),
		}
		if sensitive {
			current.value = nil
			return current, nil
		}
		if support, ok := node.(axValueSupport); ok {
			available, err := support.supportsValue(ctx, role)
			if err != nil {
				return skipUnavailable("capture: accessibility value metadata unavailable", err)
			}
			if !available {
				current.value = nil
				current.boundedValue, current.declaredBytes = false, 0
			}
		}
		childLimit := limits.MaxNodes - len(owned) + 1
		if depth >= limits.MaxDepth {
			children, more, childErr := node.children(ctx, 1)
			if childErr != nil {
				releaseAXNodes(children)
				return skipUnavailable("capture: accessibility children unavailable", childErr)
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
			return skipUnavailable("capture: accessibility children unavailable", err)
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
		path := make([]axTreeNode, len(ancestors)+1)
		copy(path, ancestors)
		path[len(ancestors)] = node
		for index, child := range children {
			// Earlier siblings can consume the remaining budget through their
			// descendants. Do not turn a bounded partial capture into an invalid
			// oversized frame when returning to this sibling list.
			if len(owned) >= limits.MaxNodes {
				truncated = true
				releaseAXNodes(children[index:])
				break
			}
			if err := ctx.Err(); err != nil {
				releaseAXNodes(children[index:])
				return nil, err
			}
			if !add(child) {
				continue
			}
			converted, err := visit(child, depth+1, path)
			if err != nil {
				releaseAXNodes(children[index+1:])
				return nil, err
			}
			current.children = append(current.children, converted)
		}
		return current, nil
	}
	frameRoot, err := visit(root, 1, nil)
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

// A retained child can outlive its container's ordinary-text classification.
// Recheck ancestors around every deferred read, even when profiles skip the
// ancestors' own values. Their native references are owned by the frame.
func guardAXAncestorValues(value func(context.Context, int) (string, error), ancestors []axTreeNode) func(context.Context, int) (string, error) {
	if len(ancestors) == 0 {
		return value
	}
	verify := func(ctx context.Context) error {
		for _, ancestor := range ancestors {
			if err := ctx.Err(); err != nil {
				return err
			}
			role, err := ancestor.role(ctx)
			if err != nil {
				return err
			}
			if role != "" && !validAXRole(role) {
				return NativeReadError(ReadStageRole, 0)
			}
			sensitive, err := ancestor.sensitive(ctx, role)
			if err != nil {
				return err
			}
			if sensitive {
				return NativeReadError(ReadStageSensitivity, 0)
			}
		}
		return ctx.Err()
	}
	return func(ctx context.Context, max int) (string, error) {
		if err := verify(ctx); err != nil {
			return "", err
		}
		text, err := value(ctx, max)
		if err != nil && !errors.Is(err, errAXValueTruncated) {
			return "", err
		}
		if verifyErr := verify(ctx); verifyErr != nil {
			return "", verifyErr
		}
		return text, err
	}
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
