//go:build darwin

package capture

import (
	"context"

	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/darwin"
)

// maxNativeTitleRead bounds the AXTitle read; longer titles are refused rather
// than truncated, so a title is either whole or absent.
const maxNativeTitleRead = 1024

// darwinTitleReader returns the frame's best-effort window-title reader. It
// reads only AXTitle of the captured window, through the same verified-target
// wrapper as the text, and returns "" on any error. The caller bounds it with
// TitleReadBudget and rejects private browser titles.
//
// Live behavior (a real AXTitle read against running apps) cannot be exercised
// in unit tests and is unverified until native acceptance on a signed build.
func darwinTitleReader(source *darwin.Adapter, probe platform.CaptureProbe, window uintptr) func(context.Context) string {
	if source == nil || window == 0 || nativeAX.api.copyValue == nil {
		return nil
	}
	return func(ctx context.Context) string {
		var title string
		err := source.WithVerifiedTarget(ctx, probe.TargetID, func(ctx context.Context, pid int32) error {
			if err := validateFocusedWindow(ctx, nativeAX.api, pid, window); err != nil {
				return err
			}
			node := &nativeAXNode{api: nativeAX.api, element: window}
			value, err := node.stringAttribute(ctx, "AXTitle", maxNativeTitleRead)
			if err != nil {
				return err
			}
			title = value
			return nil
		})
		if err != nil {
			return ""
		}
		return title
	}
}
