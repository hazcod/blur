//go:build darwin

package capture

import (
	"context"
	"errors"
	"runtime"
	"testing"
)

func TestNativeAXValueSupportUsesBoundedPositiveMetadataOnly(t *testing.T) {
	nativeAX.once.Do(loadNativeAXAPI)
	if nativeAX.err != nil {
		t.Fatal(nativeAX.err)
	}
	base := nativeAX.api
	for _, test := range []struct {
		name, role               string
		names                    []string
		status                   int32
		invalid, oversized, want bool
		calls                    int
	}{
		{name: "empty container", role: "AXGroup", names: []string{"AXRole", "AXChildren"}, want: false, calls: 1},
		{name: "custom text container", role: "AXGroup", names: []string{"AXRole", "AXValue"}, want: true, calls: 1},
		{name: "ordinary text unchanged", role: "AXStaticText", want: true},
		{name: "web range unchanged", role: "AXWebArea", want: true},
		{name: "unsupported metadata fallback", role: "AXGroup", status: axAttributeUnsupported, want: true, calls: 1},
		{name: "invalid metadata fallback", role: "AXGroup", invalid: true, want: true, calls: 1},
		{name: "oversized metadata fallback", role: "AXGroup", oversized: true, want: true, calls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			const namesArray = 0x7fff0001
			api := base
			api.setTimeout = func(uintptr, float32) int32 { return axSuccess }
			var names []uintptr
			for _, name := range test.names {
				storage, pointer := nativeCString(name)
				value := base.stringNew(0, pointer, cfUTF8)
				runtime.KeepAlive(storage)
				if value == 0 {
					t.Fatal("synthetic attribute name unavailable")
				}
				names = append(names, value)
				t.Cleanup(func() { base.release(value) })
			}
			calls, releases, inspected := 0, 0, 0
			api.copyNames = func(element uintptr, output *uintptr) int32 {
				if element != 42 {
					t.Fatal("unexpected metadata target")
				}
				calls++
				*output = namesArray
				return test.status
			}
			api.getTypeID = func(value uintptr) uintptr {
				if value == namesArray {
					return base.arrayType()
				}
				if value == 0x7fff0002 {
					return base.booleanType()
				}
				return base.getTypeID(value)
			}
			api.arrayCount = func(uintptr) int64 {
				if test.oversized {
					return maxAXAttributeNames + 1
				}
				if test.invalid {
					return 1
				}
				return int64(len(names))
			}
			api.arrayValue = func(_ uintptr, index int64) uintptr {
				inspected++
				if test.invalid {
					return 0x7fff0002
				}
				return names[index]
			}
			api.release = func(value uintptr) {
				if value == namesArray {
					releases++
					return
				}
				base.release(value)
			}
			node := &nativeAXNode{api: api, element: 42}
			got, err := node.supportsValue(context.Background(), test.role)
			if err != nil || got != test.want || calls != test.calls || releases != calls || test.oversized && inspected != 0 {
				t.Fatal("unsupported text inference or unbounded/leaked attribute metadata", got, err, calls, releases, inspected)
			}
		})
	}
}

func TestNativeAXValueSupportCancellationReleasesLateMetadata(t *testing.T) {
	nativeAX.once.Do(loadNativeAXAPI)
	if nativeAX.err != nil {
		t.Fatal(nativeAX.err)
	}
	api := nativeAX.api
	api.setTimeout = func(uintptr, float32) int32 { return axSuccess }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api.copyNames = func(_ uintptr, names *uintptr) int32 { *names = 42; cancel(); return axSuccess }
	releases := 0
	api.release = func(value uintptr) {
		if value == 42 {
			releases++
		} else {
			t.Fatal("unexpected release")
		}
	}
	node := &nativeAXNode{api: api, element: 7}
	if _, err := node.supportsValue(ctx, "AXGroup"); !errors.Is(err, context.Canceled) || releases != 1 {
		t.Fatal("late native metadata survived cancellation", err, releases)
	}
}
