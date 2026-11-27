package deletion

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

// The page adapter isolates the service's preparation loop. Actual encrypted
// record creation, paging and erasure at this scale are covered by durable's
// record-scale integration; no lifetime row budget belongs in this caller.
type pagedMeetingErasureStore struct {
	*durable.Store
	count, prepared uint64
	pages           int
	erased          bool
}

func (s *pagedMeetingErasureStore) RecoverSourceDeletion(context.Context) error { return nil }
func (s *pagedMeetingErasureStore) GetMeeting(context.Context, string) (meetings.Meeting, bool, error) {
	return meetings.Meeting{ID: "large-archive"}, true, nil
}
func (s *pagedMeetingErasureStore) MeetingRecords(_ context.Context, id string, r meetings.PageRequest) (meetings.RecordPage, error) {
	s.pages++
	if r.Limit < 1 || r.Limit > meetings.MaxPageRows || r.MaxBytes > meetings.MaxPageBytes {
		return meetings.RecordPage{}, ErrInvalid
	}
	page := meetings.RecordPage{Next: r.After}
	for seq := r.After + 1; seq <= s.count && len(page.Records) < r.Limit; seq++ {
		page.Records = append(page.Records, meetings.StoredRecord{Sequence: seq, SourceID: fmt.Sprintf("%064x", seq), Record: meetings.Record{SessionID: id}})
		page.Next = seq
	}
	page.More = page.Next < s.count
	return page, nil
}
func (s *pagedMeetingErasureStore) RecordTombstone(context.Context, durable.Tombstone) error {
	s.prepared++
	return nil
}
func (s *pagedMeetingErasureStore) DeleteMeeting(context.Context, string) error {
	if s.prepared != s.count {
		return ErrUnavailable
	}
	s.erased = true
	return nil
}

func TestMeetingDeletionPreparesEveryAcceptedRowBeyondLegacySessionBudget(t *testing.T) {
	base, err := durable.OpenDurable(filepath.Join(t.TempDir(), "erase.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	limits := meetings.DefaultLimits()
	store := &pagedMeetingErasureStore{Store: base, count: uint64(limits.MaxSegments + limits.MaxNotes + 1)}
	service, err := NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := service.DeleteMeeting(ctx, "large-archive"); err != nil {
		t.Fatalf("accepted archive could not reach erasure: prepared=%d accepted=%d pages=%d err=%v", store.prepared, store.count, store.pages, err)
	}
	if !store.erased || store.prepared != store.count || store.pages < 2 {
		t.Fatalf("preparation skipped accepted records: %+v", store)
	}
}
