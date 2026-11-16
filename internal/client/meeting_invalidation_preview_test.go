package client

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/meetings"
)

// A successful durable acknowledgement may return after its caller is canceled:
// cancellation cannot roll back a transaction that already committed. Hold that
// acknowledgement to exercise invalidation between two events in one batch.
type meetingAcknowledgementBarrier struct {
	meetings.Repository
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
	releaseOnce sync.Once
}

func (r *meetingAcknowledgementBarrier) PutMeeting(ctx context.Context, metadata meetings.Meeting, expected uint64) error {
	if err := r.Repository.PutMeeting(ctx, metadata, expected); err != nil {
		return err
	}
	if metadata.State == meetings.Running {
		r.enteredOnce.Do(func() { close(r.entered); <-r.release })
	}
	return nil
}

func TestMeetingControllerInvalidationBetweenAdmittedStateAndPreview(t *testing.T) {
	f := newControllerFixture(t)
	repo := &meetingAcknowledgementBarrier{Repository: f.store, entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { repo.releaseOnce.Do(func() { close(repo.release) }) })
	f.c.meetings.repo = repo
	id := f.start(t)
	f.audio.stream.emit(controllerState(id, 1, "running"), controllerWords(id, 1, "late-preview", "Preview admitted before deletion", false))
	select {
	case <-repo.entered:
	case <-time.After(4 * time.Second):
		t.Fatal("running acknowledgement did not reach durable commit")
	}
	invalidated := make(chan struct{})
	go func() { f.c.NotifyMemoryChanging(); close(invalidated) }()
	select {
	case <-f.audio.stream.closed:
	case <-time.After(4 * time.Second):
		t.Fatal("invalidation did not clear the live view and close the stream")
	}
	repo.releaseOnce.Do(func() { close(repo.release) })
	select {
	case <-invalidated:
	case <-time.After(4 * time.Second):
		t.Fatal("invalidation did not drain the already-admitted batch")
	}
	f.c.NotifyMemoryStable()
	f.c.meetings.wg.Wait()
	if live := f.c.MeetingLive(); live.Meeting.ID != "" || len(live.Preview) != 0 || live.State != "" {
		t.Fatalf("admitted batch republished invalidated preview: %+v", live)
	}
}
