package deletion

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

type meetingDeletionGuard struct {
	held  bool
	calls int
	err   error
}

func (g *meetingDeletionGuard) GuardSources(context.Context, []string) (func(), error) {
	return nil, errors.New("meeting deletion must guard the complete native queue")
}
func (g *meetingDeletionGuard) GuardAll(context.Context) (func(), error) {
	g.calls++
	if g.err != nil {
		return nil, g.err
	}
	g.held = true
	return func() { g.held = false }, nil
}

type observedMeetingDeleteStore struct {
	*durable.Store
	guard       *meetingDeletionGuard
	pages       int
	recovered   bool
	eraseCalled bool
	err         error
}

func (s *observedMeetingDeleteStore) RecoverSourceDeletion(ctx context.Context) error {
	if !s.guard.held {
		return errors.New("native cleanup must precede recovery of a committed meeting erasure")
	}
	s.recovered = true
	return s.Store.RecoverSourceDeletion(ctx)
}
func (s *observedMeetingDeleteStore) MeetingRecords(ctx context.Context, id string, r meetings.PageRequest) (meetings.RecordPage, error) {
	if !s.guard.held || !s.recovered || r.Limit < 1 || r.Limit > meetings.MaxPageRows || r.MaxBytes > meetings.MaxPageBytes {
		return meetings.RecordPage{}, errors.New("meeting deletion read outside its guard or page bounds")
	}
	s.pages++
	return s.Store.MeetingRecords(ctx, id, r)
}
func (s *observedMeetingDeleteStore) DeleteMeeting(ctx context.Context, id string) error {
	if !s.guard.held {
		return errors.New("native cleanup was released before erasure")
	}
	s.eraseCalled = true
	if s.err != nil {
		return s.err
	}
	return s.Store.DeleteMeeting(ctx, id)
}

func TestMeetingDeletionPagesTombstonesAndScrubsVolatileActivityBeforeJournal(t *testing.T) {
	ctx := context.Background()
	base, err := durable.OpenDurable(filepath.Join(t.TempDir(), "meeting.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = base.Close() }()
	started := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	m := meetings.Meeting{ID: "paged-erasure", Title: "Standup", Locale: "en-US", StartedAt: started, State: meetings.Running, Tracks: []string{"microphone"}, Revision: 1}
	if err := base.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	var sources []string
	for n := 0; n < meetings.MaxPageRows+3; n++ {
		segment := fmt.Sprintf("final-%d", n)
		receipt, err := base.Append(ctx, meetings.Record{Key: m.ID + "/transcript/microphone/" + segment, SessionID: m.ID, Kind: meetings.TranscriptFinal,
			Event: meetings.Event{Kind: meetings.Final, SessionID: m.ID, TrackID: "microphone", SegmentID: segment, Revision: 1, Start: time.Duration(n) * time.Second, End: time.Duration(n+1) * time.Second, Text: fmt.Sprintf("Private agenda item %d", n)}})
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, receipt.SourceID)
	}
	engine := activity.New(activity.Config{MaxGap: time.Hour, RingSize: 4})
	for n := 0; n < 2; n++ {
		if _, err := engine.Apply(activity.Event{Kind: activity.Seen, Monotonic: int64(time.Duration(n) * time.Minute), Wall: started.Add(time.Duration(n) * time.Minute), SourceID: sources[0], AppID: "meeting.app"}); err != nil {
			t.Fatal(err)
		}
	}
	guard := &meetingDeletionGuard{}
	store := &observedMeetingDeleteStore{Store: base, guard: guard}
	service, err := NewService(store, engine)
	if err != nil {
		t.Fatal(err)
	}
	service.SetNotificationCleanup(guard)
	store.err = errors.New("fixture interrupted before journal admission")
	if err := service.DeleteMeeting(ctx, m.ID); !errors.Is(err, store.err) || guard.held {
		t.Fatalf("interrupted erasure did not release its native guard: err=%v held=%t", err, guard.held)
	}
	if _, found, err := base.GetMeeting(ctx, m.ID); err != nil || !found {
		t.Fatalf("failed erasure removed meeting metadata: found=%t err=%v", found, err)
	}
	store.err = nil
	if err := service.DeleteMeeting(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if guard.held || guard.calls != 2 || !store.eraseCalled || store.pages < 4 || len(engine.Segments()) != 0 {
		t.Fatalf("cleanup was skipped or unbounded: guard=%+v pages=%d erased=%t segments=%d", guard, store.pages, store.eraseCalled, len(engine.Segments()))
	}
	log, err := base.Tombstones(ctx)
	if err != nil || len(log) != len(sources) {
		t.Fatalf("source tombstones missing: got=%d want=%d err=%v", len(log), len(sources), err)
	}
	for _, id := range sources {
		if !durable.TombstoneCovers(log, id, started) {
			t.Fatalf("source deletion is not replayable: %s", id)
		}
		if _, found, err := base.GetObservation(ctx, id); err != nil || found {
			t.Fatalf("source remains after coordinated erase: found=%t err=%v", found, err)
		}
	}
	if _, found, err := base.GetMeeting(ctx, m.ID); err != nil || found {
		t.Fatalf("meeting metadata remains after erase: found=%t err=%v", found, err)
	}
	if err := service.DeleteMeeting(ctx, m.ID); err != nil {
		t.Fatalf("retry of completed erasure failed: %v", err)
	}
}

func TestMeetingDeletionFailsBeforePreparationWhenNativeGuardFails(t *testing.T) {
	base, err := durable.OpenDurable(filepath.Join(t.TempDir(), "meeting.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = base.Close() }()
	guard := &meetingDeletionGuard{err: errors.New("fixture native cancellation failed")}
	store := &observedMeetingDeleteStore{Store: base, guard: guard}
	service, err := NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	service.SetNotificationCleanup(guard)
	if err := service.DeleteMeeting(context.Background(), "meeting"); !errors.Is(err, guard.err) || store.pages != 0 || store.recovered || store.eraseCalled {
		t.Fatalf("failed native cancellation reached storage: recovered=%t pages=%d erased=%t err=%v", store.recovered, store.pages, store.eraseCalled, err)
	}
	for _, id := range []string{"", ".", "..", "../meeting", "meeting\x00"} {
		calls := guard.calls
		if err := service.DeleteMeeting(context.Background(), id); !errors.Is(err, ErrInvalid) || guard.calls != calls {
			t.Errorf("invalid meeting ID touched native cleanup: id=%q err=%v", id, err)
		}
	}
}
