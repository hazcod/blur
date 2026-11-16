package capture

import (
	"context"
	"math"
	"testing"

	"github.com/hazcod/blur/internal/platform/darwin"
)

func TestNativeVisualWindowBindingRequiresExactAXMetadataWithoutReadingText(t *testing.T) {
	nativeAX.once.Do(loadNativeAXAPI)
	if nativeAX.err != nil {
		t.Fatal(nativeAX.err)
	}
	base := nativeAX.api
	for _, scenario := range []string{"success", "secure", "no-secure-proof", "wrong-pid", "wrong-app", "no-title", "title", "geometry", "role", "sensitivity", "unknown-children", "stale-focus", "stale-focus-after-proof", "twin-window", "offscreen-twin-window", "distinct-other-window", "unknown-window-list", "oversized-window-list", "stale-window-list", "focused-window-unlisted"} {
		t.Run(scenario, func(t *testing.T) {
			api := base
			api.copyNames = func(uintptr, *uintptr) int32 { return axAttributeUnsupported }
			api.createApp = func(int32) uintptr { return 42 }
			api.setTimeout = func(uintptr, float32) int32 { return axSuccess }
			api.secureInput = func() bool { return scenario == "secure" }
			if scenario == "no-secure-proof" {
				api.secureInput = nil
			}
			api.retain = func(value uintptr) uintptr {
				if value == 42 || value == 77 || value == 78 {
					return value
				}
				return base.retain(value)
			}
			api.release = func(value uintptr) {
				if value != 42 && value != 77 && value != 78 {
					base.release(value)
				}
			}
			api.equal = func(a, b uintptr) bool { return a == b }
			focusReads := 0
			api.copyValue = func(_ uintptr, attribute uintptr, out *uintptr) int32 {
				name, _, err := copyCFString(base, attribute, 128)
				if err != nil {
					t.Fatal(err)
				}
				switch name {
				case "AXFocusedWindow":
					focusReads++
					*out = 77
					if scenario == "stale-focus" || scenario == "stale-focus-after-proof" && focusReads > 1 {
						*out = 78
					}
					return axSuccess
				case "AXRole":
					if scenario == "role" {
						return -25204
					}
					*out, _, err = createAXAttribute(base, "AXWindow")
				case "AXSubrole":
					if scenario == "sensitivity" {
						*out, _, err = createAXAttribute(base, "AXSecureTextField")
					} else {
						return axAttributeUnsupported
					}
				case "AXTitle":
					title := "Synthetic canvas"
					if scenario == "title" {
						title = "Other window"
					}
					*out, _, err = createAXAttribute(base, title)
				case "AXPosition", "AXSize":
					*out = 1
					return axSuccess
				case "AXValue":
					t.Fatal("visual privacy proof read raw AX text")
				default:
					t.Fatalf("unexpected attribute %s", name)
				}
				if err != nil {
					t.Fatal(err)
				}
				return axSuccess
			}
			windowListReads := 0
			api.valueCount = func(_ uintptr, attribute uintptr, out *int64) int32 {
				name, _, _ := copyCFString(base, attribute, 128)
				if name == "AXWindows" {
					windowListReads++
					if scenario == "unknown-window-list" {
						return -25204
					}
					*out = 1
					if scenario == "oversized-window-list" {
						*out = maxVisualAXWindows + 1
					}
					if scenario == "twin-window" || scenario == "offscreen-twin-window" || scenario == "distinct-other-window" || scenario == "stale-window-list" && windowListReads > 1 {
						*out = 2
					}
					return axSuccess
				}
				if scenario == "unknown-children" {
					return -25204
				}
				return axAttributeUnsupported
			}
			api.copyValues = func(_, _ uintptr, _, _ int64, out *uintptr) int32 { *out = 200; return axSuccess }
			api.arrayCount = func(uintptr) int64 {
				if scenario == "twin-window" || scenario == "offscreen-twin-window" || scenario == "distinct-other-window" {
					return 2
				}
				return 1
			}
			api.arrayValue = func(_ uintptr, index int64) uintptr {
				if index > 0 || scenario == "focused-window-unlisted" {
					return 79
				}
				return 77
			}
			api.getTypeID = func(value uintptr) uintptr {
				if value == 1 {
					return 123
				}
				if value == 200 {
					return base.arrayType()
				}
				if value == 77 || value == 79 {
					return axElementTypeID()
				}
				return base.getTypeID(value)
			}
			release := api.release
			api.release = func(value uintptr) {
				if value != 1 && value != 200 {
					release(value)
				}
			}
			api.valueType = func() uintptr { return 123 }
			api.valueKind = func(uintptr) int32 { return 0 } // set below per the queried pair
			pairKind := int32(0)
			pairElement := uintptr(0)
			copyValue := api.copyValue
			api.copyValue = func(element, attribute uintptr, out *uintptr) int32 {
				name, _, _ := copyCFString(base, attribute, 128)
				if name == "AXPosition" {
					pairElement = element
					pairKind = 1
				}
				if name == "AXSize" {
					pairKind = 2
				}
				return copyValue(element, attribute, out)
			}
			api.valueKind = func(uintptr) int32 { return pairKind }
			api.valueGetPair = func(_ uintptr, kind int32, out *nativeAXPair) uint8 {
				if kind == 1 {
					*out = nativeAXPair{10, 10}
				} else {
					*out = nativeAXPair{800, 600}
				}
				if scenario == "distinct-other-window" && pairElement == 79 && kind == 1 {
					out.X = 2000
				}
				if scenario == "geometry" {
					out.X++
					out.X++
				}
				return 1
			}
			window := visualWindow()
			switch scenario {
			case "wrong-pid":
				window.PID++
			case "wrong-app":
				window.BundleID = "private.app"
			case "no-title":
				window.TitleAvailable = false
			}
			err := verifyVisualAXWindow(context.Background(), api, 42, "dev.herdr.app", 77, window, DefaultLimits())
			if (err == nil) != (scenario == "success" || scenario == "distinct-other-window") {
				t.Fatalf("window privacy/binding proof: scenario=%s err=%v", scenario, err)
			}
		})
	}
}

func TestNativeVisualGeometryRejectsNonfiniteAndMisboundWindows(t *testing.T) {
	for _, tc := range []struct {
		frame          darwin.ScreenTextRect
		position, size nativeAXPair
		ok             bool
	}{
		{darwin.ScreenTextRect{X: -1200, Y: 20, Width: 1000, Height: 700}, nativeAXPair{-1200, 20}, nativeAXPair{1000, 700}, true},
		{darwin.ScreenTextRect{Width: 1000, Height: 700}, nativeAXPair{}, nativeAXPair{1002, 700}, false},
		{darwin.ScreenTextRect{Width: 1000, Height: 700}, nativeAXPair{math.NaN(), 0}, nativeAXPair{1000, 700}, false},
		{darwin.ScreenTextRect{Width: 1000, Height: 700}, nativeAXPair{}, nativeAXPair{math.Inf(1), 700}, false},
	} {
		if visualGeometryMatches(tc.frame, tc.position, tc.size) != tc.ok {
			t.Fatal("invalid visual window geometry accepted")
		}
	}
}
