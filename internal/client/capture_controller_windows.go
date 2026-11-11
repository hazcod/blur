//go:build windows

package client

import (
	"context"
	"log/slog"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/ingest"
	platformwindows "github.com/hazcod/blur/internal/platform/windows"
)

// CaptureController wires explicit user-started Windows UI Automation capture
// to encrypted local storage. The source's WinEvent hooks are suspended on
// pause; no capture begins during application startup.
func (a *LocalApp) CaptureController(source *platformwindows.Adapter) *ingest.Controller {
	if a == nil || a.store == nil || source == nil {
		return nil
	}
	return ingest.NewController(source, func(ctx context.Context) (func(context.Context) error, error) {
		config, err := a.Client.ApplicationPreferences(ctx)
		if err != nil {
			return nil, err
		}
		pipeline := capture.NewWindowsPipeline(source, capture.DefaultLimits(), captureExclusionsFromPreferences(config), nil)
		service, err := ingest.NewWithActivityAndObservationNotify(pipeline, a.store, a.activity, a.store, a.activityEpoch, slog.Default(), a.Client.NotifyCaptureChanged, a.notifyChronicleEvidence)
		if err != nil {
			return nil, err
		}
		return service.Run, nil
	})
}
