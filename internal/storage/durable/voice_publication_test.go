package durable

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage"
)

type voiceFenceContract interface {
	BeginVoicePublicationRead() (uint64, bool)
	PublishVoiceRead(uint64, func()) bool
	AppendQuestionAtVoiceRead(context.Context, storage.QuestionRecord, uint64) (uint64, bool, error)
}

func TestVoicePublicationIgnoresDesktopCaptureButTracksVoiceMutations(t *testing.T) {
	s, _, _ := meetingStore(t)
	fence, ok := any(s).(voiceFenceContract)
	if !ok {
		t.Fatal("store has no scoped voice publication contract")
	}
	ctx := context.Background()
	m := fixtureMeeting("voice-scope")
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	first, err := s.Append(ctx, fixtureMeetingFinal(m.ID, "one", "The saved launch owner is Mira."))
	if err != nil {
		t.Fatal(err)
	}
	voice, stable := fence.BeginVoicePublicationRead()
	if !stable {
		t.Fatal("voice read unavailable")
	}
	global, stable := s.BeginPublicationRead()
	if !stable {
		t.Fatal("global read unavailable")
	}
	o, err := capture.SanitizeObservation("browser.app", "garden.example", "The unrelated gardening checklist covers watering dates.", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservationAt(ctx, o, m.StartedAt); err != nil {
		t.Fatal(err)
	}
	if !fence.PublishVoiceRead(voice, func() {}) || s.PublishRead(global, func() {}) {
		t.Fatal("desktop capture confused voice/global read scopes")
	}
	if _, err := s.Append(ctx, fixtureMeetingFinal(m.ID, "two", "The new saved phrase adds a review checklist.")); err != nil {
		t.Fatal(err)
	}
	if fence.PublishVoiceRead(voice, func() {}) {
		t.Fatal("voice append did not invalidate its prior scope")
	}
	voice, stable = fence.BeginVoicePublicationRead()
	if !stable {
		t.Fatal("voice read unavailable")
	}
	if err := s.Update(ctx, func(tx *Tx) error {
		if err := tx.PutIndexRecord(meetingMetaNS, "rolled-back-voice", []byte("uncommitted")); err != nil {
			return err
		}
		return context.Canceled
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("rollback fixture: %v", err)
	}
	if !fence.PublishVoiceRead(voice, func() {}) {
		t.Fatal("rolled-back voice write invalidated committed words")
	}
	if err := s.DeleteObservationRecord(ctx, first.SourceID); err != nil {
		t.Fatal(err)
	}
	if fence.PublishVoiceRead(voice, func() {}) {
		t.Fatal("voice source erasure retained stale read authority")
	}
	voice, stable = fence.BeginVoicePublicationRead()
	if !stable {
		t.Fatal("voice read unavailable")
	}
	if _, err := s.DeleteAllMeetings(ctx); err != nil {
		t.Fatal(err)
	}
	if fence.PublishVoiceRead(voice, func() {}) {
		t.Fatal("delete-all voice metadata retained old authority")
	}
	voice, stable = fence.BeginVoicePublicationRead()
	if !stable {
		t.Fatal("empty voice scope unavailable")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if fence.PublishVoiceRead(voice, func() {}) {
		t.Fatal("closed store published voice text")
	}
}

func TestVoiceQuestionHistoryUsesItsExactScopedToken(t *testing.T) {
	s, _, _ := meetingStore(t)
	fence, ok := any(s).(voiceFenceContract)
	if !ok {
		t.Fatal("store has no scoped voice/history publication contract")
	}
	ctx := context.Background()
	m := fixtureMeeting("voice-history-scope")
	if err := s.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, fixtureMeetingFinal(m.ID, "one", "The original launch plan requires a signed review.")); err != nil {
		t.Fatal(err)
	}
	epoch, stable := fence.BeginVoicePublicationRead()
	if !stable {
		t.Fatal("voice scope unavailable")
	}
	q := storage.QuestionRecord{Question: "What did I say in my voice note?", AskedAt: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC), PlanKind: "lookup"}
	next, accepted, err := fence.AppendQuestionAtVoiceRead(ctx, q, epoch)
	if err != nil || !accepted || next != epoch || !fence.PublishVoiceRead(next, func() {}) {
		t.Fatalf("own history changed voice authority: %d %v %v", next, accepted, err)
	}
	if _, err := s.Append(ctx, fixtureMeetingFinal(m.ID, "two", "The revised launch note adds a second review.")); err != nil {
		t.Fatal(err)
	}
	if _, accepted, err := fence.AppendQuestionAtVoiceRead(ctx, q, next); err != nil || accepted {
		t.Fatalf("stale voice answer was refreshed by history: %v %v", accepted, err)
	}
	rows, err := s.ListQuestions(ctx, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("stale question persisted: %+v %v", rows, err)
	}
}
