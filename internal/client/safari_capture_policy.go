package client

import (
	"context"
	"time"

	"github.com/hazcod/blur/internal/browsercapture"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/preferences"
)

// The final check and both new-observation/sighting commits run under the
// preference Store's Save mutex. Direct preference API changes therefore guard
// the old Safari pipeline even before the controller's pause/restart finishes.
func safariCapturePreferencePolicy(store *preferences.Store) browsercapture.CommitPolicy {
	return func(ctx context.Context, observation capture.SanitizedObservation, write func(context.Context) error) error {
		if store == nil || ctx == nil || write == nil {
			return browsercapture.ErrRejected
		}
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		return store.WithConfig(ctx, func(config preferences.Config) error {
			if !config.CaptureEnabled || !config.SafariPageCaptureEnabled || !capture.TargetAllowed(captureExclusionsFromPreferences(config), observation.AppID(), observation.Domain()) {
				return browsercapture.ErrRejected
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			return write(ctx)
		})
	}
}
