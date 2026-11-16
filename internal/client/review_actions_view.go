package client

import (
	"context"
	"slices"

	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/tasks"
)

func (d *desktop) dismissTodayTask(id string) {
	if d.loading || d.client == nil {
		return
	}
	date := d.today.Date
	var accepted tasks.TodayCandidate
	for _, todo := range d.today.Snapshot.TodoCandidates {
		if todo.ID == id {
			accepted = todayTaskCandidate(date, todo)
			break
		}
	}
	if accepted.ID == "" {
		return
	}
	epoch, stable := d.client.BeginMemoryRead()
	if !stable {
		return
	}
	d.loading, d.status = true, "Dismissing suggestion…"
	d.app.RequestUpdate()
	go func() {
		err := d.client.dismissTodayTaskAtEpoch(context.Background(), accepted, epoch)
		d.app.Post(func() {
			if !d.client.PublishMemoryRead(epoch, func() {
				d.loading = false
				if err != nil {
					d.status = "The suggestion could not be dismissed. Refresh Today and try again."
				} else {
					d.status = "Suggestion dismissed. Its source is still saved."
				}
			}) {
				return
			}
			if err == nil && d.date == date {
				d.loadToday()
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) confirmLibraryFact(id string, expected knowledge.Fact) {
	if d.loading || d.client == nil || expected.State != knowledge.Proposed {
		return
	}
	epoch, stable := d.client.BeginMemoryRead()
	if !stable {
		return
	}
	expected.Sources = slices.Clone(expected.Sources)
	d.loading, d.status = true, "Confirming memory…"
	d.app.RequestUpdate()
	go func() {
		err := d.client.confirmInferredFactAtEpoch(context.Background(), id, expected, epoch)
		// Confirmation invalidates older reads. Reload through the new epoch;
		// no accepted plaintext is copied back out of this worker.
		finishedEpoch, stable := d.client.BeginMemoryRead()
		d.app.Post(func() {
			if !stable || !d.client.PublishMemoryRead(finishedEpoch, func() {
				d.loading = false
				if err != nil {
					d.status = "The memory could not be confirmed. Refresh Library and review it again."
				} else {
					d.status = "Memory confirmed with its original source."
					d.libraryEditFact = ""
					d.libraryLoaded = false
				}
			}) {
				return
			}
			if err == nil && d.page == "Library" {
				d.loadLibrary()
				d.inspectLibraryEntity(id, 0)
			}
			d.app.RequestUpdate()
		})
	}()
}
