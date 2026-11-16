package capture

import (
	"context"
	"errors"
	"sync"
)

var (
	errBrowserContentUnavailable = errors.New("capture: active browser page unavailable")
	errBrowserPageChanged        = errors.New("capture: active browser page changed")
)

const browserMetadataNodes = 192
const browserMetadataDepth = 80

// browserTreeNode exposes only structural/location metadata during page
// selection. Its text callback is never invoked until capture policy passes.
type browserTreeNode interface {
	axTreeNode
	hidden(context.Context) (bool, error)
	location(context.Context) (string, bool, error)
	childMatches(context.Context, int, int, uintptr) (bool, error)
}

type browserPageEdge struct {
	parent       browserTreeNode
	index, total int
	child        uintptr
}

// A lease retains the selected web area's structural path. Capture walks
// only that content subtree. Revalidation checks the recorded edges instead
// of repeating a whole-window search for every text value.
type browserPageLease struct {
	window      browserTreeNode
	root        browserTreeNode
	path        []browserPageEdge
	owned       []axTreeNode
	transferred bool
	once        sync.Once
}

func findBrowserPage(ctx context.Context, window browserTreeNode, limits Limits) (result *browserPageLease, err error) {
	if ctx == nil || window == nil || !limits.valid() {
		return nil, errBrowserContentUnavailable
	}
	lease := &browserPageLease{window: window}
	type pageCandidate struct {
		root browserTreeNode
		path []browserPageEdge
	}
	var candidates []pageCandidate
	defer func() {
		if err != nil {
			lease.release()
		}
	}()
	type entry struct {
		node  browserTreeNode
		depth int
		path  []browserPageEdge
	}
	queue := []entry{{node: window, depth: 1}}
	seen := map[uintptr]bool{window.identity(): true}
	// Browser chrome can be much deeper than the document's own tree. Give
	// page discovery its separate finite depth budget, with the same node and
	// deadline bounds and no content reads. The document starts at the web area.
	maxNodes, maxDepth := min(limits.MaxNodes, browserMetadataNodes), browserMetadataDepth
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current := queue[0]
		queue = queue[1:]
		role, err := current.node.role(ctx)
		if err != nil {
			return nil, errBrowserContentUnavailable
		}
		if browserChromeSubtree(role) {
			continue
		}
		hidden, err := current.node.hidden(ctx)
		if err != nil {
			return nil, errBrowserContentUnavailable
		}
		if hidden {
			continue
		}
		if role == "AXWebArea" {
			candidates = append(candidates, pageCandidate{root: current.node, path: current.path})
			continue // Nested web areas are content of this outer page, not alternate tabs.
		}
		remaining := maxNodes - len(seen)
		children, more, err := current.node.children(ctx, max(1, remaining))
		if err != nil || more || len(children) > remaining || current.depth >= maxDepth && len(children) > 0 {
			releaseAXNodes(children)
			return nil, errBrowserContentUnavailable
		}
		for index, child := range children {
			if child == nil {
				releaseAXNodes(children[index+1:])
				return nil, errBrowserContentUnavailable
			}
			if seen[child.identity()] {
				child.release()
				continue
			}
			seen[child.identity()] = true
			lease.owned = append(lease.owned, child)
			node, ok := child.(browserTreeNode)
			if !ok {
				releaseAXNodes(children[index+1:])
				return nil, errBrowserContentUnavailable
			}
			path := append([]browserPageEdge(nil), current.path...)
			path = append(path, browserPageEdge{parent: current.node, index: index, total: len(children), child: child.identity()})
			queue = append(queue, entry{node: node, depth: current.depth + 1, path: path})
		}
	}
	if len(candidates) == 1 {
		lease.root, lease.path = candidates[0].root, candidates[0].path
	} else if len(candidates) > 1 {
		// Some browser AX trees expose several non-hidden WebAreas at once. The
		// browser window's current document URL can disambiguate them by host;
		// same-host tabs and missing metadata remain ambiguous and fail closed.
		activeDomain, activeKnown, locationErr := window.location(ctx)
		if locationErr != nil || !activeKnown || activeDomain == "" {
			return nil, errBrowserContentUnavailable
		}
		matches := 0
		for _, candidate := range candidates {
			domain, known, err := candidate.root.location(ctx)
			if err != nil {
				return nil, errBrowserContentUnavailable
			}
			if known && domain == activeDomain {
				lease.root, lease.path = candidate.root, candidate.path
				matches++
			}
		}
		if matches != 1 {
			lease.root, lease.path = nil, nil
			return nil, errBrowserContentUnavailable
		}
	}
	if lease.root == nil {
		return nil, errBrowserContentUnavailable
	}
	return lease, nil
}

func browserChromeSubtree(role string) bool {
	if isEditableTextRole(role) {
		return true
	}
	switch role {
	case "AXToolbar", "AXMenuBar", "AXMenu", "AXMenuItem", "AXTab", "AXButton", "AXImage", "AXStaticText", "AXLink", "AXHeading", "AXScrollBar", "AXStatusBar":
		return true
	default:
		return false
	}
}

func (p *browserPageLease) takeRoot() browserTreeNode {
	if p.transferred {
		return nil
	}
	p.transferred = true
	return p.root
}

func (p *browserPageLease) release() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		for i := len(p.owned) - 1; i >= 0; i-- {
			node := p.owned[i]
			if p.transferred && p.root != nil && node.identity() == p.root.identity() {
				continue
			}
			node.release()
		}
	})
}

func (p *browserPageLease) validate(ctx context.Context) error {
	if p == nil || p.root == nil {
		return errBrowserPageChanged
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, edge := range p.path {
		hidden, err := edge.parent.hidden(ctx)
		if err != nil || hidden {
			return errBrowserPageChanged
		}
		matches, err := edge.parent.childMatches(ctx, edge.index, edge.total, edge.child)
		if err != nil || !matches {
			return errBrowserPageChanged
		}
	}
	hidden, err := p.root.hidden(ctx)
	if err != nil || hidden {
		return errBrowserPageChanged
	}
	return nil
}

func (p *browserPageLease) domain(ctx context.Context) (string, bool, error) {
	page, known, err := p.root.location(ctx)
	if err != nil {
		return "", false, err
	}
	window, windowKnown, err := p.window.location(ctx)
	if err != nil {
		return "", false, err
	}
	if known && windowKnown && page != window {
		return "", false, errCaptureDomainChanged
	}
	if known {
		return page, true, nil
	}
	return window, windowKnown, nil
}
