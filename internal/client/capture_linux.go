//go:build linux

package client

import (
	"context"
	"log/slog"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/ingest"
	platformlinux "github.com/hazcod/blur/internal/platform/linux"
)

// CaptureController connects explicit user start/pause to encrypted storage.
// AT-SPI capture remains dormant until the user starts it in the UI.
func (a *LocalApp) CaptureController(source *platformlinux.Adapter) *ingest.Controller {
	if a == nil || a.store == nil || source == nil {
		return nil
	}
	return ingest.NewController(source, func(ctx context.Context) (func(context.Context) error, error) {
		config, err := a.Client.ApplicationPreferences(ctx)
		if err != nil {
			return nil, err
		}
		pipeline := capture.NewLinuxPipeline(source, capture.DefaultLimits(), captureExclusionsFromPreferences(config), nil)
		service, err := ingest.NewWithActivityAndObservationNotify(pipeline, a.store, a.activity, a.store, a.activityEpoch, slog.Default(), a.Client.NotifyCaptureChanged, a.notifyChronicleEvidence)
		if err != nil {
			return nil, err
		}
		return service.Run, nil
	})
}
