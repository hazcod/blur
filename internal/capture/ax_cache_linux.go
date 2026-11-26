//go:build linux

package capture

import (
	"context"
	"errors"
	"sort"

	"github.com/godbus/dbus/v5"
	platformlinux "github.com/hazcod/blur/internal/platform/linux"
)

const (
	atspiCacheIface = "org.a11y.atspi.Cache"
	atspiCachePath  = dbus.ObjectPath("/org/a11y/atspi/cache")
	// AT-SPI has no bounded cache API: one GetItems reply carries the whole
	// application tree, including windows the capture will never walk. The
	// cap bounds what Blur parses and retains; a larger reply rejects the
	// capture instead of silently truncating the tree.
	atspiCacheEntryCap   = 8192
	atspiCacheStateWords = 8
)

// atspiCacheItem matches AT-SPI's GetItems reply signature
// ((so)(so)(so)iiassusau): reference, application, parent, index in parent,
// child count, interfaces, name, role, description, states.
type atspiCacheItem struct {
	Ref         atspiCacheRefWire
	App         atspiCacheRefWire
	Parent      atspiCacheRefWire
	Index       int32
	ChildCount  int32
	Interfaces  []string
	Name        string
	Role        uint32
	Description string
	States      []uint32
}

type atspiCacheRefWire struct {
	Name string
	Path dbus.ObjectPath
}

func validateATSPICacheItems(items []atspiCacheItem) error {
	if len(items) > atspiCacheEntryCap {
		return errors.New("capture: AT-SPI cache exceeds capture budget")
	}
	for _, item := range items {
		if len(item.States) > atspiCacheStateWords || len(item.Interfaces) > atspiMaxInterfaces {
			return errors.New("capture: AT-SPI cache exceeds capture budget")
		}
	}
	return nil
}

// fetchATSPICache reads the application's accessibility cache in one reply.
func fetchATSPICache(ctx context.Context, conn *dbus.Conn, bus string) ([]atspiCacheItem, error) {
	if conn == nil || !platformlinux.ValidAccessibleObject(bus, atspiCachePath) {
		return nil, errors.New("capture: AT-SPI cache unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var items []atspiCacheItem
	if err := conn.Object(bus, atspiCachePath).CallWithContext(ctx, atspiCacheIface+".GetItems", 0).Store(&items); err != nil {
		return nil, errors.New("capture: AT-SPI cache unavailable")
	}
	if err := validateATSPICacheItems(items); err != nil {
		return nil, err
	}
	return items, nil
}

type atspiCacheKey struct {
	bus  string
	path dbus.ObjectPath
}

type atspiCacheTree struct {
	conn     *dbus.Conn
	entries  map[atspiCacheKey]atspiCacheItem
	children map[atspiCacheKey][]atspiCacheKey
}

// buildATSPICacheTree turns the reply into a parent-keyed tree. Parent edges
// that point outside the cache, such as the application root's desktop
// parent on the registry, do not become child links.
func buildATSPICacheTree(conn *dbus.Conn, items []atspiCacheItem) (*atspiCacheTree, error) {
	tree := &atspiCacheTree{
		conn: conn, entries: make(map[atspiCacheKey]atspiCacheItem, len(items)),
		children: make(map[atspiCacheKey][]atspiCacheKey, len(items)),
	}
	for _, item := range items {
		if !platformlinux.ValidAccessibleObject(item.Ref.Name, item.Ref.Path) {
			return nil, errors.New("capture: AT-SPI cache object invalid")
		}
		key := atspiCacheKey{item.Ref.Name, item.Ref.Path}
		if _, duplicate := tree.entries[key]; duplicate {
			return nil, errors.New("capture: AT-SPI cache object duplicate")
		}
		tree.entries[key] = item
	}
	for _, item := range items {
		key := atspiCacheKey{item.Ref.Name, item.Ref.Path}
		parent := atspiCacheKey{item.Parent.Name, item.Parent.Path}
		if key == parent {
			continue
		}
		if _, ok := tree.entries[parent]; !ok {
			continue
		}
		tree.children[parent] = append(tree.children[parent], key)
	}
	// Order each child list by the reported index so the frame follows the
	// application's own reading order.
	for _, keys := range tree.children {
		order := keys
		sort.SliceStable(order, func(i, j int) bool {
			return tree.entries[order[i]].Index < tree.entries[order[j]].Index
		})
	}
	return tree, nil
}

func (t *atspiCacheTree) node(key atspiCacheKey) (*atspiCachedNode, bool) {
	item, ok := t.entries[key]
	if !ok {
		return nil, false
	}
	id := nextATSPINodeID.Add(1)
	if id == 0 {
		return nil, false
	}
	return &atspiCachedNode{tree: t, entry: item, id: uintptr(id)}, true
}

// atspiCachedNode exposes one cache entry to the shared walker. Structure and
// role come from the cache reply; live text, when the entry advertises the
// Text interface, is still read through the same bounded live path as the
// classic walker, so the lease and privacy revalidations around value reads
// apply unchanged.
type atspiCachedNode struct {
	tree  *atspiCacheTree
	entry atspiCacheItem
	id    uintptr
}

func (n *atspiCachedNode) identity() uintptr { return n.id }
func (*atspiCachedNode) release()            {}

func (n *atspiCachedNode) role(context.Context) (string, error) {
	name, _ := platformlinux.RoleForCapture(n.entry.Role)
	return name, nil
}

func (n *atspiCachedNode) sensitive(_ context.Context, role string) (bool, error) {
	return isEditableTextRole(role), nil
}

func (n *atspiCachedNode) children(ctx context.Context, max int) ([]axTreeNode, bool, error) {
	if max <= 0 {
		return nil, false, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	keys := n.tree.children[atspiCacheKey{n.entry.Ref.Name, n.entry.Ref.Path}]
	limited, more, err := boundedATSPIChildCount(int32(len(keys)), max)
	if err != nil {
		return nil, false, err
	}
	nodes := make([]axTreeNode, 0, limited)
	for _, key := range keys[:limited] {
		if err := ctx.Err(); err != nil {
			releaseAXNodes(nodes)
			return nil, more, err
		}
		child, ok := n.tree.node(key)
		if !ok {
			releaseAXNodes(nodes)
			return nil, more, errors.New("capture: AT-SPI cache object missing")
		}
		nodes = append(nodes, child)
	}
	return nodes, more, nil
}

func (n *atspiCachedNode) value(ctx context.Context, max int) (string, error) {
	if max <= 0 {
		return "", errAXValueTruncated
	}
	if !atspiCacheItemHasText(n.entry) {
		// Static accessibles carry their visible text as the cache name.
		return boundedATSPICacheName(n.entry.Name, max)
	}
	if text, err := readATSPITextValue(ctx, n.tree.conn, n.entry.Ref.Name, n.entry.Ref.Path, max); err == nil || errors.Is(err, errAXValueTruncated) {
		return text, err
	}
	// GTK4 advertises text interfaces in the cache that it does not expose
	// live on the object; the cache name remains the readable fallback.
	return boundedATSPICacheName(n.entry.Name, max)
}

func atspiCacheItemHasText(item atspiCacheItem) bool {
	for _, name := range item.Interfaces {
		if name == atspiTextIface {
			return true
		}
	}
	return false
}

func boundedATSPICacheName(name string, max int) (string, error) {
	bounded, truncated, err := boundedATSPIText(name, max, false)
	if err != nil {
		return "", err
	}
	if truncated {
		return bounded, errAXValueTruncated
	}
	return bounded, nil
}

// atspiWalkerAvailable reports whether the window's accessibles implement the
// classic per-object tree protocol. GTK4 windows publish through the AT-SPI
// cache and implement none of the child accessors.
func atspiWalkerAvailable(ctx context.Context, conn *dbus.Conn, bus string, path dbus.ObjectPath) bool {
	if conn == nil || !platformlinux.ValidAccessibleObject(bus, path) {
		return false
	}
	if err := ctx.Err(); err != nil {
		return false
	}
	var count int32
	return conn.Object(bus, path).CallWithContext(ctx, atspiAccessibleIface+".GetChildCount", 0).Store(&count) == nil
}

// collectLinuxCacheFrame reads the verified window's subtree from the
// application's AT-SPI cache. Entries outside the active window's subtree,
// including other windows of the same application, are never walked.
func collectLinuxCacheFrame(ctx context.Context, target platformlinux.FocusedContext, limits Limits) (rawFrame, error) {
	items, err := fetchATSPICache(ctx, target.Conn, target.WindowBus)
	if err != nil {
		return rawFrame{}, err
	}
	tree, err := buildATSPICacheTree(target.Conn, items)
	if err != nil {
		return rawFrame{}, err
	}
	root, ok := tree.node(atspiCacheKey{target.WindowBus, target.WindowPath})
	if !ok {
		return rawFrame{}, errors.New("capture: AT-SPI cache window unavailable")
	}
	return collectAXFrame(ctx, root, limits)
}
