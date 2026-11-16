package capture

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/darwin"
)

// NewDarwinPipelineWithVisualText retains the ordinary AX path and adds a
// foreground-only local fallback for complete, nonsensitive empty AX frames.
func NewDarwinPipelineWithVisualText(source *darwin.Adapter, limits Limits, exclusions Exclusions, profiles []AppProfile, reader VisualTextReader) Pipeline {
	p := NewDarwinPipeline(source, limits, exclusions, profiles)
	original := p.Read
	p.Read = func(ctx context.Context, probe platform.CaptureProbe, limits Limits) (rawFrame, error) {
		frame, err := original(ctx, probe, limits)
		if err != nil || reader == nil {
			return frame, err
		}
		frame.visual = func(ctx context.Context, max int, admit visualAdmission) (string, bool, error) {
			if frame.browserRegion != nil {
				return readVisualRegion(ctx, reader, frame.browserRegion, fmt.Sprintf("capture-%d", visualRequestSequence.Add(1)), max, admit)
			}
			verify := func(ctx context.Context, candidate darwin.ScreenTextWindow) error {
				return source.WithVerifiedTarget(ctx, probe.TargetID, func(ctx context.Context, pid int32) error {
					return verifyVisualAXWindow(ctx, nativeAX.api, pid, probe.AppID, frame.nativeRootID, candidate, limits)
				})
			}
			return readVisualText(ctx, reader, verify, fmt.Sprintf("capture-%d", visualRequestSequence.Add(1)), max, admit)
		}
		return frame, nil
	}
	return p
}

type nativeAXPair struct{ X, Y float64 }

func (n *nativeAXNode) visualPair(ctx context.Context, name string, kind int32) (nativeAXPair, error) {
	var pair nativeAXPair
	value, status, err := n.copyAttribute(ctx, name)
	if err != nil {
		return pair, err
	}
	if value == 0 || status != axSuccess {
		return pair, captureError(Excluded, nil)
	}
	defer n.api.release(value)
	if n.api.valueGetPair == nil || n.api.getTypeID(value) != n.api.valueType() || n.api.valueKind(value) != kind || n.api.valueGetPair(value, kind, &pair) == 0 || math.IsNaN(pair.X) || math.IsNaN(pair.Y) || math.IsInf(pair.X, 0) || math.IsInf(pair.Y, 0) {
		return nativeAXPair{}, captureError(Excluded, nil)
	}
	return pair, ctx.Err()
}

// verifyVisualAXWindow requires a fresh, complete AX privacy walk. It does not
// read any AX text values. A CG topmost window alone never authorizes capture.
func verifyVisualAXWindow(ctx context.Context, api nativeAXAPI, pid int32, app string, expected uintptr, candidate darwin.ScreenTextWindow, limits Limits) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if api.secureInput == nil || api.secureInput() {
		return captureError(Excluded, nil)
	}
	if candidate.PID != pid || !strings.EqualFold(candidate.BundleID, app) || !candidate.TitleAvailable || candidate.TitleTruncated {
		return captureError(Excluded, nil)
	}
	if err := validateFocusedWindow(ctx, api, pid, expected); err != nil {
		return captureError(Excluded, err)
	}
	root := &nativeAXNode{api: api, element: api.retain(expected)}
	// collectAXFrame owns this extra reference on both success and failure.
	frame, err := collectAXFrame(ctx, root, limits)
	if err != nil {
		return captureError(Excluded, err)
	}
	defer frame.cleanup()
	nodes, err := validateFrame(frame, limits)
	if err != nil || !visualFrameSafe(rawFrame{roots: frame.roots, truncated: frame.truncated, validate: func(context.Context) error { return nil }}, nodes) {
		return captureError(Excluded, nil)
	}
	title, err := root.stringAttribute(ctx, "AXTitle", 512)
	if err != nil || title != candidate.Title {
		return captureError(Excluded, nil)
	}
	position, err := root.visualPair(ctx, "AXPosition", 1) // kAXValueCGPointType
	if err != nil {
		return err
	}
	size, err := root.visualPair(ctx, "AXSize", 2) // kAXValueCGSizeType
	if err != nil {
		return err
	}
	if !visualGeometryMatches(candidate.Frame, position, size) {
		return captureError(Excluded, nil)
	}
	if err := verifyVisualAXWindowUnique(ctx, api, pid, expected, candidate.Frame); err != nil {
		return err
	}
	if api.secureInput() {
		return captureError(Excluded, nil)
	}
	if err := validateFocusedWindow(ctx, api, pid, expected); err != nil {
		return captureError(Excluded, err)
	}
	return nil
}

func visualGeometryMatches(frame darwin.ScreenTextRect, position, size nativeAXPair) bool {
	// AX and CG use global display points; allow rounding of at most one point.
	for _, pair := range [][2]float64{{frame.X, position.X}, {frame.Y, position.Y}, {frame.Width, size.X}, {frame.Height, size.Y}} {
		if math.IsNaN(pair[0]) || math.IsInf(pair[0], 0) || math.IsNaN(pair[1]) || math.IsInf(pair[1], 0) || math.Abs(pair[0]-pair[1]) > 1 {
			return false
		}
	}
	return frame.Width > 0 && frame.Height > 0 && size.X > 0 && size.Y > 0
}
