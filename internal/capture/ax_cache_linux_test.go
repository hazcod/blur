//go:build linux

package capture

import (
	"context"
	"errors"
	"testing"

	"github.com/godbus/dbus/v5"
)

func cacheItem(bus string, path, parent string, index int32, role uint32, name string, interfaces []string) atspiCacheItem {
	return atspiCacheItem{
		Ref:    atspiCacheRefWire{Name: bus, Path: dbus.ObjectPath(path)},
		App:    atspiCacheRefWire{Name: bus, Path: dbus.ObjectPath("/org/a11y/atspi/accessible/root")},
		Parent: atspiCacheRefWire{Name: bus, Path: dbus.ObjectPath(parent)},
		Index:  index,
		Role:   role,
		Name:   name, Interfaces: interfaces,
	}
}

func TestATSPICacheTreeBuildsOnlyParentEdgesInsideTheCache(t *testing.T) {
	items := []atspiCacheItem{
		cacheItem(":1.10", "/app", "/org/a11y/atspi/accessible/root", 0, 23, "Window", nil),
		cacheItem(":1.10", "/second", "/app", 2, 73, "second", nil),
		cacheItem(":1.10", "/first", "/app", 1, 73, "first", nil),
		cacheItem(":1.10", "/zero", "/app", 0, 73, "zero", nil),
	}
	tree, err := buildATSPICacheTree(nil, items)
	if err != nil {
		t.Fatal(err)
	}
	key := atspiCacheKey{":1.10", "/app"}
	children := tree.children[key]
	if len(children) != 3 {
		t.Fatalf("window children = %d, want 3", len(children))
	}
	wantOrder := []string{"zero", "first", "second"}
	for i, childKey := range children {
		if got := tree.entries[childKey].Name; got != wantOrder[i] {
			t.Fatalf("child %d = %q, want %q (index ordering)", i, got, wantOrder[i])
		}
	}
}

func TestATSPICacheTreeRejectsDuplicateObjects(t *testing.T) {
	items := []atspiCacheItem{
		cacheItem(":1.10", "/app", "/root", 0, 23, "Window", nil),
		cacheItem(":1.10", "/app", "/root", 0, 23, "Window", nil),
	}
	if _, err := buildATSPICacheTree(nil, items); err == nil {
		t.Fatal("duplicate cache objects were accepted")
	}
}

func TestATSPICacheTreeRejectsInvalidObjects(t *testing.T) {
	items := []atspiCacheItem{cacheItem("not a bus name", "/app", "/root", 0, 23, "Window", nil)}
	if _, err := buildATSPICacheTree(nil, items); err == nil {
		t.Fatal("invalid cache objects were accepted")
	}
}

func TestValidateATSPICacheItemsBoundsReply(t *testing.T) {
	oversize := make([]atspiCacheItem, atspiCacheEntryCap+1)
	if err := validateATSPICacheItems(oversize); err == nil {
		t.Fatal("oversize cache reply was accepted")
	}
	wideStates := []atspiCacheItem{{States: make([]uint32, atspiCacheStateWords+1)}}
	if err := validateATSPICacheItems(wideStates); err == nil {
		t.Fatal("oversize state word list was accepted")
	}
}

func TestATSPICachedNodeChildrenRespectBoundAndMissingObjects(t *testing.T) {
	items := []atspiCacheItem{
		cacheItem(":1.10", "/app", "/root", 0, 23, "Window", nil),
		cacheItem(":1.10", "/one", "/app", 0, 73, "one", nil),
		cacheItem(":1.10", "/two", "/app", 1, 73, "two", nil),
	}
	tree, err := buildATSPICacheTree(nil, items)
	if err != nil {
		t.Fatal(err)
	}
	root, ok := tree.node(atspiCacheKey{":1.10", "/app"})
	if !ok {
		t.Fatal("window node missing from cache tree")
	}
	nodes, more, err := root.children(context.Background(), 1)
	if err != nil || len(nodes) != 1 || !more {
		t.Fatalf("bounded children = %d more=%t err=%v, want 1 more=true", len(nodes), more, err)
	}
	// A child key without an entry fails the walk closed instead of
	// silently skipping content.
	delete(tree.entries, atspiCacheKey{":1.10", "/two"})
	if _, _, err := root.children(context.Background(), 2); err == nil {
		t.Fatal("missing cache object did not fail the walk")
	}
}

func TestATSPICachedNodeValueUsesBoundedNameAndLiveTextFallback(t *testing.T) {
	label := cacheItem(":1.10", "/label", "/app", 0, 73, "Saved note", nil)
	tree, err := buildATSPICacheTree(nil, []atspiCacheItem{label})
	if err != nil {
		t.Fatal(err)
	}
	node, ok := tree.node(atspiCacheKey{":1.10", "/label"})
	if !ok {
		t.Fatal("label node missing from cache tree")
	}
	text, err := node.value(context.Background(), 256)
	if err != nil || text != "Saved note" {
		t.Fatalf("label value = %q err=%v, want cache name", text, err)
	}
	long := cacheItem(":1.10", "/long", "/app", 0, 73, string(make([]byte, 64))+string(make([]byte, 64)), nil)
	if _, err := boundedATSPICacheName(long.Name, 32); !errors.Is(err, errAXValueTruncated) {
		t.Fatalf("oversize cache name was not truncated: %v", err)
	}
	// A text-interface entry whose live object is unavailable falls back to
	// the bounded cache name instead of failing the capture.
	withText := cacheItem(":1.10", "/text", "/app", 0, 73, "fallback", []string{atspiTextIface})
	tree.entries[atspiCacheKey{":1.10", "/text"}] = withText
	textNode, ok := tree.node(atspiCacheKey{":1.10", "/text"})
	if !ok {
		t.Fatal("text node missing from cache tree")
	}
	if text, err := textNode.value(context.Background(), 256); err != nil || text != "fallback" {
		t.Fatalf("live-text fallback value = %q err=%v, want cache name fallback", text, err)
	}
}

func TestATSPICachedNodePrunesEditableRoles(t *testing.T) {
	node := &atspiCachedNode{entry: cacheItem(":1.10", "/entry", "/app", 0, 61, "typing", []string{atspiTextIface})}
	role, err := node.role(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sensitive, err := node.sensitive(context.Background(), role)
	if err != nil || !sensitive {
		t.Fatalf("editable cache role %q was not pruned (sensitive=%t err=%v)", role, sensitive, err)
	}
	secure := &atspiCachedNode{entry: cacheItem(":1.10", "/secret", "/app", 0, 40, "hunter2", nil)}
	secureRole, err := secure.role(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sensitive, err := secure.sensitive(context.Background(), secureRole); err != nil || !sensitive {
		t.Fatalf("secure cache role %q was not pruned (sensitive=%t err=%v)", secureRole, sensitive, err)
	}
}
