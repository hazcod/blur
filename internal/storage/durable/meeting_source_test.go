package durable

import (
	"context"
	"errors"
	"testing"

	"github.com/hazcod/blur/internal/capture"
)

func TestMeetingForSourceResolvesOnlyAuthenticatedLiveMembership(t *testing.T) {
	s, _, _ := meetingStore(t)
	ctx := context.Background()
	m := fixtureMeeting("source-owner")
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	r := fixtureMeetingFinal(m.ID, "one", "Mira owns rollback.")
	receipt, err := s.Append(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	resolved, found, err := s.MeetingForSource(ctx, receipt.SourceID)
	if err != nil || !found || resolved.ID != m.ID || resolved.RecordCount != 1 {
		t.Fatal("real source membership did not resolve", resolved, found, err)
	}
	spoof, err := capture.SanitizeObservation(meetingAppID, "forged.meeting.local", "Looks like a voice source", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservation(ctx, spoof); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.MeetingForSource(ctx, spoof.ContentID()); err != nil || found {
		t.Fatal("app/domain spoof granted meeting membership", found, err)
	}
	if err := s.DeleteMeeting(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.MeetingForSource(ctx, receipt.SourceID); err != nil || found {
		t.Fatal("deleted source resolved its old session", found, err)
	}
}

func TestMeetingForSourceRejectsMisdirectedAuthenticatedReverseEdge(t *testing.T) {
	s, _, _ := meetingStore(t)
	ctx := context.Background()
	m := fixtureMeeting("wrong-source-owner")
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	first, err := s.Append(ctx, fixtureMeetingFinal(m.ID, "one", "First saved words."))
	if err != nil {
		t.Fatal(err)
	}
	r := fixtureMeetingFinal(m.ID, "two", "Second saved words.")
	if _, err := s.Append(ctx, r); err != nil {
		t.Fatal(err)
	}
	key := s.meetingDigest("key", []byte(r.Key))
	if err := s.Update(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		return tx.putIndexValueLocked(meetingSourceNS, first.SourceID, []byte(key))
	}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.MeetingForSource(ctx, first.SourceID); found || !errors.Is(err, ErrCorrupt) {
		t.Fatal("mismatched reverse edge granted membership", found, err)
	}
}
