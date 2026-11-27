package durable

import (
	"context"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/storage"
)

func TestReadPublicationRejectsCommittedWritesWithoutWaitingForActiveWriter(t *testing.T) {
	s, _, _ := rollupTestStore(t, 0xD8)
	epoch, stable := s.BeginPublicationRead()
	if !stable {
		t.Fatal("new store unavailable")
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- s.Update(context.Background(), func(tx *Tx) error {
			close(entered)
			<-release
			return tx.PutIndexRecord("sentinel", "publication", []byte("committed"))
		})
	}()
	<-entered
	result := make(chan bool, 1)
	go func() { result <- s.PublishRead(epoch, func() { t.Error("published during active writer") }) }()
	select {
	case accepted := <-result:
		if accepted {
			t.Error("active writer admitted publication")
		}
	case <-time.After(time.Second):
		close(release)
		<-done
		t.Fatal("publication blocked on writer")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s.PublishRead(epoch, func() { t.Error("published before intervening commit") }) {
		t.Fatal("stale token survived commit")
	}
	current, stable := s.BeginPublicationRead()
	if !stable {
		t.Fatal("committed store unavailable")
	}
	called := false
	if !s.PublishRead(current, func() { called = true }) || !called {
		t.Fatal("current result not installed")
	}
	// A failed transaction must not invalidate a committed read.
	if err := s.Update(context.Background(), func(tx *Tx) error {
		if err := tx.PutIndexRecord("sentinel", "publication", []byte("rolled back")); err != nil {
			return err
		}
		return context.Canceled
	}); err == nil {
		t.Fatal("fixture write unexpectedly committed")
	}
	if !s.PublishRead(current, func() {}) {
		t.Fatal("rollback invalidated committed read")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s.PublishRead(current, func() { t.Error("published closed store") }) {
		t.Fatal("closed store admitted publication")
	}
}

func TestQuestionHistoryAdvancesOnlyItsMatchingRead(t *testing.T) {
	s, _, _ := rollupTestStore(t, 0xD9)
	writer, ok := any(s).(interface {
		AppendQuestionAtRead(context.Context, storage.QuestionRecord, uint64) (uint64, bool, error)
	})
	if !ok {
		t.Fatal("question history cannot preserve an exact read token")
	}
	epoch, stable := s.BeginPublicationRead()
	if !stable {
		t.Fatal("store unavailable")
	}
	record := storage.QuestionRecord{Question: "How much time in 2024?", AskedAt: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC), PlanKind: "time"}
	next, accepted, err := writer.AppendQuestionAtRead(context.Background(), record, epoch)
	if err != nil || !accepted || !s.PublishRead(next, func() {}) || s.PublishRead(epoch, func() {}) {
		t.Fatalf("history lost read identity: %d %v %v", next, accepted, err)
	}
	// A commit between the activity read and its own history write cannot be
	// mistaken for that history write and adopted as a fresh read token.
	if err := s.Update(context.Background(), func(tx *Tx) error { return tx.PutIndexRecord("sentinel", "history-read", []byte("changed")) }); err != nil {
		t.Fatal(err)
	}
	_, accepted, err = writer.AppendQuestionAtRead(context.Background(), record, next)
	if err != nil || accepted {
		t.Fatalf("stale answer retokened: %v %v", accepted, err)
	}
	rows, err := s.ListQuestions(context.Background(), 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("stale question persisted: %+v %v", rows, err)
	}
}
