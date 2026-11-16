//go:build darwin

package capture

import (
	"context"
	"runtime"
	"testing"
)

// TestReadNativeFocusWalksFakeAXAPI drives the real native adapter through a
// fake AX API: element 50 (text field) in group 51 in group 52 in window 77.
// The fake fails the test if AXValue is ever requested.
func TestReadNativeFocusWalksFakeAXAPI(t *testing.T) {
	nativeAX.once.Do(loadNativeAXAPI)
	if nativeAX.err != nil {
		t.Fatal(nativeAX.err)
	}
	base := nativeAX.api
	api := base
	api.createApp = func(int32) uintptr { return 42 }
	api.setTimeout = func(uintptr, float32) int32 { return axSuccess }
	api.equal = func(a, b uintptr) bool { return a == b }
	api.getTypeID = func(v uintptr) uintptr {
		if v >= 42 && v <= 77 {
			return axElementTypeID()
		}
		return base.getTypeID(v)
	}
	api.release = func(v uintptr) {
		if v < 100 {
			return
		}
		base.release(v)
	}
	str := func(s string) uintptr {
		storage, ptr := nativeCString(s)
		out := base.stringNew(0, ptr, cfUTF8)
		runtime.KeepAlive(storage)
		return out
	}
	roles := map[uintptr]string{50: "AXTextField", 51: "AXGroup", 52: "AXGroup", 77: "AXWindow"}
	titles := map[uintptr]string{50: "Subject", 51: "Header", 52: "Compose", 77: "Private window title"}
	parents := map[uintptr]uintptr{50: 51, 51: 52, 52: 77}
	api.copyValue = func(element, attribute uintptr, out *uintptr) int32 {
		name, _, err := copyCFString(base, attribute, 64)
		if err != nil {
			t.Fatal(err)
		}
		switch name {
		case "AXFocusedUIElement":
			*out = 50
			return axSuccess
		case "AXRole":
			*out = str(roles[element])
			return axSuccess
		case "AXTitle":
			if element == 77 {
				t.Fatal("window title was read")
			}
			*out = str(titles[element])
			return axSuccess
		case "AXDescription", "AXSubrole":
			return axNoValue
		case "AXParent":
			*out = parents[element]
			return axSuccess
		default:
			t.Fatalf("unexpected AX attribute %q (value must never be read)", name)
			return axAttributeUnsupported
		}
	}
	got, ok := readNativeFocus(context.Background(), api, 7, 77)
	if !ok || got.role != "AXTextField" || got.name != "Subject" || len(got.ancestors) != 2 || got.ancestors[0] != "Compose" || got.ancestors[1] != "Header" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
	if _, ok := readNativeFocus(context.Background(), api, 7, 99); ok {
		t.Fatal("focus outside the captured window was accepted")
	}
}

func TestNativeFocusNodeRefusesValueAttributes(t *testing.T) {
	node := nativeFocusNode{node: &nativeAXNode{}}
	for _, name := range []string{"AXValue", "AXSelectedText", "AXChildren", "AXVisibleText"} {
		if _, err := node.attribute(context.Background(), name, 16); err == nil {
			t.Fatalf("%s was readable through the breadcrumb adapter", name)
		}
	}
}
