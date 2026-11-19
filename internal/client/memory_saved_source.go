package client

import (
	"context"
	"time"

	"github.com/hazcod/blur/internal/storage"
)

// revealSavedMemorySource refreshes saved browsing without changing either
// composer's draft, opening a popup, or inventing a source from unsaved text.
func (d *desktop) revealSavedMemorySource(sourceID string) {
	if d == nil || d.client == nil || sourceID == "" {
		return
	}
	u := &d.memoryUI
	u.loaded, u.previous = false, nil
	epoch, stable := d.client.BeginMemoryRead()
	if !stable {
		return
	}
	generation := d.memoryGeneration
	go func() {
		ctx, done, err := d.client.beginOperation(context.Background(), shortOperationTimeout)
		if err != nil {
			return
		}
		defer done()
		source, found, err := storage.ReadSourceEvidence(ctx, d.client.data, sourceID)
		if err != nil || !found {
			return
		}
		at, unknown, err := d.client.sourceCaptureTime(ctx, sourceID, time.Time{})
		if err != nil {
			return
		}
		hit := SearchHit{SourceID: source.SourceID(), AppID: source.AppID(), Domain: source.Domain(), Text: source.Text(), CapturedAt: at, TimeUnknown: unknown, Partial: source.Truncated(), Truncated: source.Truncated(), WindowTitle: source.Title()}
		d.app.Post(func() {
			if generation != d.memoryGeneration {
				return
			}
			if !d.client.PublishMemoryRead(epoch, func() { u.revealedSource = &hit }) {
				return
			}
			if d.page == "Ask" && d.askQuestion == "" && !u.loading {
				d.loadMemoryBrowse(memoryBrowseCursor{})
			}
			d.app.RequestUpdate()
		})
	}()
}
