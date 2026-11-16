//go:build !darwin

package capture

import (
	"context"
	"github.com/hazcod/blur/internal/platform"
	"time"
)

func supportsDocumentSource(platform.CaptureSource) bool { return false }
func openPlatformDocument(context.Context, Pipeline, platform.CaptureProbe, string, string, Exclusions, time.Time, func(string) bool) (DocumentJob, error) {
	return nil, ErrDocumentUnsupported
}
