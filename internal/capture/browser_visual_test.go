package capture

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/hazcod/blur/internal/platform/darwin"
)

func TestBrowserViewportClipsTallPageToVerifiedAncestorsAndRejectsChrome(t *testing.T) {
	for _, scenario := range []string{"success", "unknown-ancestor", "unknown-chrome", "overlapping-chrome", "fractional-overlap-chrome", "empty-ancestor", "nonfinite-page", "outside-page", "whole-window", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			windowFrame := darwin.ScreenTextRect{X: -1400, Y: -800, Width: 1000, Height: 700}
			viewport := darwin.ScreenTextRect{X: -1380, Y: -700, Width: 960, Height: 580}
			page := browserFixture(4, "AXWebArea")
			scroll := browserFixture(3, "AXScrollArea", page)
			toolbar := browserFixture(2, "AXToolbar")
			window := browserFixture(1, "AXWindow", toolbar, scroll)
			lease, err := findBrowserPage(context.Background(), window, DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			defer lease.release()
			geometry := map[uintptr]darwin.ScreenTextRect{1: windowFrame, 2: {X: -1400, Y: -800, Width: 1000, Height: 80}, 3: viewport, 4: {X: -1380, Y: -1200, Width: 960, Height: 5000}}
			switch scenario {
			case "unknown-ancestor":
				delete(geometry, 3)
			case "unknown-chrome":
				delete(geometry, 2)
			case "overlapping-chrome":
				geometry[2] = viewport
			case "fractional-overlap-chrome":
				geometry[2] = darwin.ScreenTextRect{X: -1400, Y: -800, Width: 1000, Height: 100.5}
			case "empty-ancestor":
				geometry[3] = darwin.ScreenTextRect{}
			case "nonfinite-page":
				geometry[4] = darwin.ScreenTextRect{X: math.NaN(), Y: -700, Width: 960, Height: 580}
			case "outside-page":
				geometry[4] = darwin.ScreenTextRect{X: 5000, Y: 5000, Width: 960, Height: 580}
			case "whole-window":
				geometry[3], geometry[4] = windowFrame, windowFrame
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "cancel" {
				cancel()
			}
			region, err := browserPageViewport(ctx, lease, windowFrame, func(ctx context.Context, node axTreeNode) (darwin.ScreenTextRect, error) {
				if err := ctx.Err(); err != nil {
					return darwin.ScreenTextRect{}, err
				}
				r, ok := geometry[node.identity()]
				if !ok {
					return r, captureError(Excluded, nil)
				}
				return r, nil
			})
			if scenario == "success" {
				if err != nil || region != viewport {
					t.Fatal("tall page viewport lost clipping or top-left coordinates", region, err)
				}
			} else if err == nil {
				t.Fatal("unverified browser region accepted", region)
			}
			if page.valueReads != 0 || toolbar.valueReads != 0 {
				t.Fatal("region proof read source/chrome values")
			}
		})
	}
}

func TestBrowserViewportRejectsUnclippedChromeDescendants(t *testing.T) {
	for _, scenario := range []string{"overlap", "unknown", "fractional", "hidden-parent", "safe", "oversize", "cycle"} {
		t.Run(scenario, func(t *testing.T) {
			page := browserFixture(4, "AXWebArea")
			scroll := browserFixture(3, "AXScrollArea", page)
			overflow := browserFixture(5, "AXStaticText")
			toolbar := browserFixture(2, "AXToolbar", overflow)
			window := browserFixture(1, "AXWindow", toolbar, scroll)
			bounds := map[uintptr]darwin.ScreenTextRect{
				1: {X: 0, Y: 0, Width: 1000, Height: 700},
				2: {X: 0, Y: 0, Width: 1000, Height: 80},
				3: {X: 0, Y: 100, Width: 1000, Height: 600},
				4: {X: 0, Y: 100, Width: 1000, Height: 600},
				5: {X: 0, Y: 100, Width: 400, Height: 100},
			}
			switch scenario {
			case "unknown":
				delete(bounds, 5)
			case "fractional":
				bounds[5] = darwin.ScreenTextRect{X: 0, Y: 99.5, Width: 400, Height: 1}
			case "hidden-parent":
				toolbar.hiddenValue = true
			case "safe", "oversize", "cycle":
				bounds[5] = darwin.ScreenTextRect{X: 0, Y: 0, Width: 400, Height: 80}
			}
			if scenario == "oversize" {
				for i := 0; i < browserMetadataNodes; i++ {
					toolbar.childrenNodes = append(toolbar.childrenNodes, browserFixture(uintptr(10+i), "AXStaticText"))
				}
			}
			if scenario == "cycle" {
				overflow.childrenNodes = []*browserFixtureNode{toolbar}
			}
			lease, err := findBrowserPage(context.Background(), window, DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			defer lease.release()
			region, err := browserPageViewport(context.Background(), lease, bounds[1], func(_ context.Context, node axTreeNode) (darwin.ScreenTextRect, error) {
				return bounds[node.identity()], nil
			})
			if scenario == "safe" {
				if err != nil || region != bounds[4] || toolbar.childReads == 0 {
					t.Fatal("complete safe chrome metadata not verified", region, err)
				}
			} else if err == nil {
				t.Fatal("crop accepted unverified chrome descendants", region)
			}
			if toolbar.valueReads != 0 || overflow.valueReads != 0 || page.valueReads != 0 || page.childReads != 0 {
				t.Fatal("chrome proof read source values or document descendants")
			}
		})
	}
}

type syntheticRegionReader struct {
	*syntheticVisualReader
	request darwin.ScreenTextRequest
	modify  func(*darwin.ScreenTextResult)
}

func (r *syntheticRegionReader) RecognizeWindow(ctx context.Context, request darwin.ScreenTextRequest) (darwin.ScreenTextResult, error) {
	r.request = request
	result, err := r.syntheticVisualReader.RecognizeWindow(ctx, request)
	if request.Region != nil {
		region := *request.Region
		result.Region = &region
	}
	if r.modify != nil {
		r.modify(&result)
	}
	return result, err
}

func TestBrowserVisualReaderRequiresSameExactCropBeforeAndAfterAcquisition(t *testing.T) {
	for _, scenario := range []string{"success", "no-permission", "whole-window", "outside-window", "private-before", "changed-before-acquisition", "changed-after-acquisition", "missing-echo", "wrong-echo", "denied-admission", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			reader := &syntheticRegionReader{syntheticVisualReader: &syntheticVisualReader{window: visualWindow(), text: "Only verified page content"}}
			region := darwin.ScreenTextRect{X: 20, Y: 100, Width: 780, Height: 500}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "no-permission":
				reader.window.PermissionGranted = false
			case "whole-window":
				region = reader.window.Frame
			case "outside-window":
				region.X = -1
			case "missing-echo":
				reader.modify = func(result *darwin.ScreenTextResult) { result.Region = nil }
			case "wrong-echo":
				reader.modify = func(result *darwin.ScreenTextResult) { result.Region.X++ }
			case "cancel":
				reader.modify = func(*darwin.ScreenTextResult) { cancel() }
			}
			checks := 0
			verify := func(context.Context, darwin.ScreenTextWindow) (darwin.ScreenTextRect, error) {
				checks++
				if scenario == "private-before" {
					return darwin.ScreenTextRect{}, captureError(Excluded, nil)
				}
				next := region
				if scenario == "changed-before-acquisition" && checks == 2 || scenario == "changed-after-acquisition" && checks == 3 {
					next.Y++
				}
				return next, nil
			}
			admit := func(ctx context.Context, acquire func(context.Context) error) error {
				if scenario == "denied-admission" {
					return captureError(Excluded, nil)
				}
				return acquire(ctx)
			}
			text, partial, err := readVisualRegion(ctx, reader, verify, "region", 4096, admit)
			if scenario == "success" {
				if err != nil || text != reader.text || !partial || checks != 3 || reader.request.Region == nil || *reader.request.Region != region {
					t.Fatal("verified browser crop lost bounded authority/coverage", err, checks)
				}
				return
			}
			if err == nil || text != "" {
				t.Fatal("changed/private/uncropped browser output escaped", err)
			}
			if scenario == "no-permission" || scenario == "whole-window" || scenario == "outside-window" || scenario == "private-before" || scenario == "changed-before-acquisition" || scenario == "denied-admission" {
				if reader.reads != 0 {
					t.Fatal("pixels acquired before exact browser authority")
				}
			}
			if scenario == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("caller cancellation lost", err)
			}
		})
	}
}
