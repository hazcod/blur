package client

import (
	"context"
	"time"

	"github.com/hazcod/blur/internal/preferences"
)

const capturePauseUnsavedCopy = "Capture is paused for this session, but the pause could not be saved. It may resume when you reopen Blur. Retry saving the pause."

func (d *desktop) saveCapturePause() {
	if d == nil || d.client == nil || d.app == nil || d.loading || !d.capturePauseUnsaved || d.capture.Running() {
		return
	}
	next := d.preferences
	next.CaptureEnabled = false
	d.status = "Saving capture pause…"
	d.persistApplicationPreferences(next, func(err error) {
		if err != nil {
			d.status = capturePauseUnsavedCopy
			return
		}
		d.preferences = next
		d.capturePauseUnsaved = false
		d.captureState = "paused"
		d.status = "Capture is paused."
		d.updateMenuBarCaptureState()
	})
}

// Preference persistence may wait for a bounded capture acquisition or encrypted
// write. Keep that wait off the UI thread, including infrequently used actions.
func (d *desktop) persistApplicationPreferences(next preferences.Config, complete func(error)) {
	d.loading = true
	d.app.RequestUpdate()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		err := d.client.ConfigureApplicationPreferences(ctx, next)
		cancel()
		d.app.Post(func() {
			d.loading = false
			complete(err)
			d.app.RequestUpdate()
		})
	}()
}
