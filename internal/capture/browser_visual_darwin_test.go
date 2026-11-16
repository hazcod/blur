package capture

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/darwin"
	"github.com/hazcod/blur/internal/platform/fake"
)

type browserVisualAXObject struct {
	role, title, url string
	frame            darwin.ScreenTextRect
	children         []uintptr
}

func TestNativeBrowserRegionRequiresCurrentPrivateDomainPageAndGeometryProof(t *testing.T) {
	for _, scenario := range []string{"success", "private", "domain-changed", "page-changed", "page-changed-during-geometry", "private-during-geometry", "secure-input", "secure-child", "unknown-geometry", "chrome-overlap"} {
		t.Run(scenario, func(t *testing.T) {
			nativeAX.once.Do(loadNativeAXAPI)
			if nativeAX.err != nil {
				t.Fatal(nativeAX.err)
			}
			base, api := nativeAX.api, nativeAX.api
			candidate := visualWindow()
			candidate.BundleID, candidate.Title = "com.kagi.kagimacos", "Synthetic browser"
			viewport := darwin.ScreenTextRect{X: 10, Y: 100, Width: 800, Height: 500}
			objects := map[uintptr]*browserVisualAXObject{
				42: {role: "AXApplication"},
				77: {role: "AXWindow", title: candidate.Title, url: "https://allowed.example/document", frame: candidate.Frame, children: []uintptr{78, 79}},
				78: {role: "AXToolbar", frame: darwin.ScreenTextRect{X: 10, Y: 10, Width: 800, Height: 80}},
				79: {role: "AXScrollArea", frame: viewport, children: []uintptr{80}},
				80: {role: "AXWebArea", url: "https://allowed.example/document", frame: darwin.ScreenTextRect{X: 10, Y: -500, Width: 800, Height: 5000}},
				81: {role: "AXWebArea", url: "https://allowed.example/other", frame: viewport},
				82: {role: "AXSecureTextField", frame: viewport},
			}
			refs, arrays := map[uintptr]int{}, map[uintptr][]uintptr{}
			pairKinds, pairs := map[uintptr]int32{}, map[uintptr]nativeAXPair{}
			next := uintptr(10000)
			api.createApp = func(int32) uintptr { refs[42]++; return 42 }
			api.setTimeout = func(uintptr, float32) int32 { return axSuccess }
			api.secureInput = func() bool { return scenario == "secure-input" }
			api.copyNames = func(uintptr, *uintptr) int32 { return axAttributeUnsupported }
			api.retain = func(value uintptr) uintptr {
				if objects[value] != nil {
					refs[value]++
					return value
				}
				return base.retain(value)
			}
			api.release = func(value uintptr) {
				if objects[value] != nil {
					refs[value]--
					if refs[value] < 0 {
						t.Fatal("AX reference released twice")
					}
					return
				}
				if _, ok := arrays[value]; ok {
					delete(arrays, value)
					return
				}
				if _, ok := pairKinds[value]; ok {
					delete(pairKinds, value)
					delete(pairs, value)
					return
				}
				base.release(value)
			}
			api.getTypeID = func(value uintptr) uintptr {
				if objects[value] != nil {
					return axElementTypeID()
				}
				if _, ok := arrays[value]; ok {
					return base.arrayType()
				}
				if _, ok := pairKinds[value]; ok {
					return base.valueType()
				}
				return base.getTypeID(value)
			}
			api.equal = func(a, b uintptr) bool {
				if objects[a] != nil || objects[b] != nil {
					return a == b
				}
				return base.equal(a, b)
			}
			api.arrayCount = func(value uintptr) int64 { return int64(len(arrays[value])) }
			api.arrayValue = func(value uintptr, index int64) uintptr { return arrays[value][index] }
			name := func(attribute uintptr) string {
				text, _, err := copyCFString(base, attribute, 64)
				if err != nil {
					t.Fatal(err)
				}
				return text
			}
			api.valueCount = func(element, attribute uintptr, out *int64) int32 {
				if name(attribute) == "AXWindows" {
					*out = 1
				} else {
					*out = int64(len(objects[element].children))
				}
				return axSuccess
			}
			api.copyValues = func(element, attribute uintptr, start, count int64, out *uintptr) int32 {
				children := objects[element].children
				if name(attribute) == "AXWindows" {
					children = []uintptr{77}
				}
				next++
				arrays[next] = append([]uintptr(nil), children[start:start+count]...)
				*out = next
				return axSuccess
			}
			api.valueKind = func(value uintptr) int32 { return pairKinds[value] }
			api.valueGetPair = func(value uintptr, kind int32, out *nativeAXPair) uint8 {
				if pairKinds[value] != kind {
					return 0
				}
				*out = pairs[value]
				return 1
			}
			changed := false
			api.copyValue = func(element, attribute uintptr, out *uintptr) int32 {
				object := objects[element]
				attributeName, text := name(attribute), ""
				switch attributeName {
				case "AXFocusedWindow":
					refs[77]++
					*out = 77
					return axSuccess
				case "AXRole":
					text = object.role
				case "AXSubrole", "AXHidden":
					return axAttributeUnsupported
				case "AXTitle":
					text = object.title
				case "AXDocument", "AXURL":
					if object.url == "" {
						return axAttributeUnsupported
					}
					text = object.url
				case "AXPosition", "AXSize":
					if scenario == "unknown-geometry" && element == 80 {
						return axAttributeUnsupported
					}
					next++
					kind := int32(1)
					pair := nativeAXPair{X: object.frame.X, Y: object.frame.Y}
					if attributeName == "AXSize" {
						kind = 2
						pair = nativeAXPair{X: object.frame.Width, Y: object.frame.Height}
					}
					pairKinds[next], pairs[next], *out = kind, pair, next
					if element == 80 && attributeName == "AXSize" && !changed {
						changed = true
						if scenario == "page-changed-during-geometry" {
							objects[79].children = []uintptr{81}
						}
						if scenario == "private-during-geometry" {
							objects[77].title = "Private Browsing"
						}
					}
					return axSuccess
				case "AXValue":
					t.Fatal("browser pixel proof read source/chrome text")
				default:
					t.Fatal("unexpected native metadata attribute", attributeName)
				}
				storage, pointer := nativeCString(text)
				*out = base.stringNew(0, pointer, cfUTF8)
				runtime.KeepAlive(storage)
				return axSuccess
			}
			window := &nativeAXNode{api: api, element: api.retain(77)}
			original, err := findBrowserPage(context.Background(), window, DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				original.release()
				window.release()
				for _, count := range refs {
					if count != 0 {
						t.Fatal("AX crop proof leaked reference", refs)
					}
				}
				if len(arrays) != 0 || len(pairs) != 0 {
					t.Fatal("AX crop proof leaked copied metadata")
				}
			})
			switch scenario {
			case "private":
				objects[77].title = "Private Browsing"
			case "domain-changed":
				objects[80].url, objects[77].url = "https://private.example", "https://private.example"
			case "page-changed":
				objects[79].children = []uintptr{81}
			case "secure-child":
				objects[80].children = []uintptr{82}
			case "chrome-overlap":
				objects[78].frame = viewport
			}
			region, err := verifyBrowserVisualRegion(context.Background(), api, candidate.PID, candidate.BundleID, 77, original, "allowed.example", candidate, DefaultLimits())
			if scenario == "success" {
				if err != nil || region != viewport {
					t.Fatal("verified native browser viewport rejected", region, err)
				}
				reader := &syntheticRegionReader{syntheticVisualReader: &syntheticVisualReader{window: candidate, text: "Verified canvas body\napi_key=synthetic-secret"}}
				limits := DefaultLimits()
				limits.MaxBytes = 4096
				p := Pipeline{Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "synthetic", AppID: candidate.BundleID}}, Limits: limits,
					Read: func(context.Context, platform.CaptureProbe, Limits) (rawFrame, error) {
						proof := func(ctx context.Context, window darwin.ScreenTextWindow) (darwin.ScreenTextRect, error) {
							return verifyBrowserVisualRegion(ctx, api, candidate.PID, candidate.BundleID, 77, original, "allowed.example", window, limits)
						}
						return rawFrame{roots: []*rawNode{{role: "AXWebArea", declaredBytes: 0}}, domain: "allowed.example", domainKnown: true,
							validate: original.validate, validateDomain: func(ctx context.Context, expected string) error {
								domain, known, err := original.domain(ctx)
								if err != nil {
									return err
								}
								if !known || domain != expected {
									return errCaptureDomainChanged
								}
								return nil
							}, browserRegion: proof, visual: func(ctx context.Context, max int, admit visualAdmission) (string, bool, error) {
								return readVisualRegion(ctx, reader, proof, "browser-pipeline", max, admit)
							}}, nil
					},
				}
				got, err := p.Capture(context.Background())
				if err != nil || reader.reads != 1 || !got.Truncated() || !strings.Contains(got.Text(), "Verified canvas body") || strings.Contains(got.Text(), "synthetic-secret") || reader.request.Region == nil || *reader.request.Region != viewport {
					t.Fatal("native browser crop did not reach bounded sanitized visible-only observation", err, reader.reads)
				}
			} else if err == nil {
				t.Fatal("stale/private/unverified native page authorized pixels", region)
			}
		})
	}
}
