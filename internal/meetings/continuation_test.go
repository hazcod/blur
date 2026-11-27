package meetings

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The lookup port stands in for a disk index; this fixture deliberately keeps
// its own records so eviction in the engine cannot hide a conflicting retry.
type indexedFixtureSink struct {
	*testSink
	deleted map[string]bool
}

func (s *indexedFixtureSink) LookupRecord(_ context.Context, record Record) (Receipt, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleted[record.Key] {
		return Receipt{}, false, ErrDeleted
	}
	old, found := s.records[record.Key]
	if !found {
		return Receipt{}, false, nil
	}
	if old != record {
		return Receipt{}, false, ErrConflict
	}
	return Receipt{SourceID: "source-" + record.Key}, true, nil
}
func indexedRunning(t *testing.T) (*Session, Token, *indexedFixtureSink) {
	t.Helper()
	sink := &indexedFixtureSink{testSink: encryptedFixtureSink(), deleted: map[string]bool{}}
	s, err := New(context.Background(), Config{SessionID: "meeting-1", Tracks: []string{"microphone"}}, sink)
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s, token, sink
}

func TestDurableContinuationKeepsHotStateBoundedBeyondFormerSessionCeilings(t *testing.T) {
	s, token, sink := indexedRunning(t)
	ctx := context.Background()
	// Cross both former 32,768-final and 16MiB text ceilings independently.
	for i := 0; i < 32770; i++ {
		text := "word"
		if i < 1100 {
			text = strings.Repeat("v", 16<<10)
		}
		e := event("microphone", fmt.Sprintf("segment-%d", i), 1, time.Duration(i)*time.Second, time.Duration(i+1)*time.Second, text, Final)
		if err := s.Submit(ctx, token, e); err != nil {
			t.Fatalf("final %d: %v", i, err)
		}
	}
	for i := 0; i < 300; i++ {
		if err := s.AddNote(ctx, ManualNoteInput{ID: fmt.Sprintf("note-%d", i), Text: strings.Repeat("n", 4096)}); err != nil {
			t.Fatalf("note %d: %v", i, err)
		}
	}
	snap := s.Snapshot()
	if len(snap.Finals) == 0 || len(snap.Finals) > 20 || len(snap.Notes) == 0 || len(snap.Notes) > 20 {
		t.Fatalf("unbounded or empty hot state: finals=%d notes=%d", len(snap.Finals), len(snap.Notes))
	}
	if len(sink.records) != 33070 {
		t.Fatalf("durable evidence lost: %d", len(sink.records))
	}
	if err := s.Pause(ctx); err != nil {
		t.Fatal(err)
	}
	token, err := s.Resume(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Submit(ctx, token, event("microphone", "after-resume", 1, 40*time.Hour, 40*time.Hour+time.Second, "Still recording", Final)); err != nil {
		t.Fatalf("long offset/resume: %v", err)
	}
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestEvictedFinalConsultsDurableIdentityBeforeOrdering(t *testing.T) {
	for _, mode := range []string{"duplicate", "partial", "conflict", "deleted"} {
		t.Run(mode, func(t *testing.T) {
			s, token, sink := indexedRunning(t)
			first := event("microphone", "first", 1, 0, time.Second, "Original evidence", Final)
			for i := 0; i < 30; i++ {
				e := event("microphone", fmt.Sprintf("later-%d", i), 1, time.Duration(i+1)*time.Second, time.Duration(i+2)*time.Second, "Later evidence", Final)
				if i == 0 {
					e = first
				}
				if err := s.Submit(context.Background(), token, e); err != nil {
					t.Fatal(err)
				}
			}
			var want error
			switch mode {
			case "partial":
				first.Kind = Partial
				want = ErrOrder
			case "conflict":
				first.Text = "Changed evidence"
				want = ErrConflict
			case "deleted":
				sink.deleted["meeting-1/transcript/microphone/first"] = true
				want = ErrDeleted
			}
			if err := s.Submit(context.Background(), token, first); !errors.Is(err, want) {
				t.Fatalf("old key returned %v, want %v", err, want)
			}
			if len(sink.records) != 30 {
				t.Fatal("retry appended or revived evidence")
			}
		})
	}
}

func TestDurableLookupAlsoFencesHotDuplicatesAndEvictedNotes(t *testing.T) {
	s, token, sink := indexedRunning(t)
	first := event("microphone", "first", 1, 0, time.Second, "First", Final)
	if err := s.Submit(context.Background(), token, first); err != nil {
		t.Fatal(err)
	}
	sink.deleted["meeting-1/transcript/microphone/first"] = true
	if err := s.Submit(context.Background(), token, first); !errors.Is(err, ErrDeleted) {
		t.Fatalf("hot duplicate bypassed deletion fence: %v", err)
	}
	s, _, sink = indexedRunning(t)
	for i := 0; i < 30; i++ {
		if err := s.AddNote(context.Background(), ManualNoteInput{ID: fmt.Sprintf("n-%d", i), Text: "Original"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddNote(context.Background(), ManualNoteInput{ID: "n-0", Text: "Original"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddNote(context.Background(), ManualNoteInput{ID: "n-0", Text: "Changed"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("evicted note conflict: %v", err)
	}
}

func TestContinuationPreservesDurableAdmissionErrorsAfterLookup(t *testing.T) {
	for _, want := range []error{ErrDeleted, ErrLimit} {
		t.Run(want.Error(), func(t *testing.T) {
			s, token, sink := indexedRunning(t)
			sink.append = func(context.Context, Record) (Receipt, error) { return Receipt{}, want }
			if err := s.Submit(context.Background(), token, event("microphone", "raced", 1, 0, time.Second, "Evidence", Final)); !errors.Is(err, want) {
				t.Fatalf("durable admission error hidden: %v", err)
			}
			snap := s.Snapshot()
			if snap.State != Failed || snap.Pending == nil || len(snap.Finals) != 0 {
				t.Fatal("failed durable admission discarded exact pending evidence")
			}
		})
	}
}

func TestContinuationHonorsExplicitBudgetsAfterHotTailEviction(t *testing.T) {
	sink := &indexedFixtureSink{testSink: encryptedFixtureSink(), deleted: map[string]bool{}}
	s, err := New(context.Background(), Config{SessionID: "meeting-1", Tracks: []string{"microphone"}, Limits: Limits{MaxSegments: 24}}, sink)
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 24; i++ {
		if err := s.Submit(context.Background(), token, event("microphone", fmt.Sprintf("s-%d", i), 1, time.Duration(i)*time.Second, time.Duration(i+1)*time.Second, "Evidence", Final)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Submit(context.Background(), token, event("microphone", "excess", 1, 24*time.Second, 25*time.Second, "Evidence", Final)); !errors.Is(err, ErrLimit) {
		t.Fatal("explicit lifetime guard counted only hot tail", err)
	}
}

func TestContinuationRejectsRepresentationOverflowBeforeAppend(t *testing.T) {
	for _, kind := range []string{"final-count", "final-bytes", "note-count", "note-bytes"} {
		t.Run(kind, func(t *testing.T) {
			s, token, sink := indexedRunning(t)
			var err error
			switch kind {
			case "final-count":
				s.finalCount = ^uint64(0)
			case "final-bytes":
				s.transcriptBytes = ^uint64(0) - 1
			case "note-count":
				s.noteCount = ^uint64(0)
			case "note-bytes":
				s.totalManualBytes = ^uint64(0) - 1
			}
			if strings.HasPrefix(kind, "note") {
				err = s.AddNote(context.Background(), ManualNoteInput{ID: "excess", Text: "Overflow"})
			} else {
				err = s.Submit(context.Background(), token, event("microphone", "excess", 1, 0, time.Second, "Overflow", Final))
			}
			if !errors.Is(err, ErrLimit) || len(sink.records) != 0 {
				t.Fatalf("counter overflow reached persistence: %v", err)
			}
		})
	}
}

type failedLookupSink struct {
	*indexedFixtureSink
	err error
}

func (s *failedLookupSink) LookupRecord(ctx context.Context, r Record) (Receipt, bool, error) {
	if s.err != nil {
		return Receipt{}, false, s.err
	}
	return s.indexedFixtureSink.LookupRecord(ctx, r)
}
func TestContinuationLookupCancellationKeepsTypedCauseAndRedactsAdapterText(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			sink := &failedLookupSink{indexedFixtureSink: &indexedFixtureSink{testSink: encryptedFixtureSink(), deleted: map[string]bool{}}, err: fmt.Errorf("private evidence: %w", cause)}
			s, err := New(context.Background(), Config{SessionID: "meeting-1", Tracks: []string{"microphone"}}, sink)
			if err != nil {
				t.Fatal(err)
			}
			token, err := s.Start(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			err = s.Submit(context.Background(), token, event("microphone", "new", 1, 0, time.Second, "Evidence", Final))
			if !errors.Is(err, ErrCanceled) || !errors.Is(err, cause) || strings.Contains(err.Error(), "private evidence") {
				t.Fatalf("lookup cancellation changed typed/redacted error contract: %v", err)
			}
		})
	}
}

func TestContinuationFinalizingPartialCannotOverflowFinalCounter(t *testing.T) {
	s, token, sink := indexedRunning(t)
	e := event("microphone", "last", 1, 0, time.Second, "Final representation", Partial)
	if err := s.Submit(context.Background(), token, e); err != nil {
		t.Fatal(err)
	}
	// Other admitted tracks may have advanced the counter while this partial
	// remained volatile. Finalization must still check the increment itself.
	s.finalCount = ^uint64(0)
	e.Kind = Final
	if err := s.Submit(context.Background(), token, e); !errors.Is(err, ErrLimit) || len(sink.records) != 0 {
		t.Fatalf("partial finalization overflowed: %v", err)
	}
}

type blockedLookupSink struct {
	*indexedFixtureSink
	entered chan struct{}
	release chan struct{}
}

func (s *blockedLookupSink) LookupRecord(ctx context.Context, r Record) (Receipt, bool, error) {
	close(s.entered)
	<-s.release
	return s.indexedFixtureSink.LookupRecord(ctx, r)
}
func TestContinuationSlowLookupDoesNotDelayEpochInvalidation(t *testing.T) {
	sink := &blockedLookupSink{indexedFixtureSink: &indexedFixtureSink{testSink: encryptedFixtureSink(), deleted: map[string]bool{}}, entered: make(chan struct{}), release: make(chan struct{})}
	s, err := New(context.Background(), Config{SessionID: "meeting-1", Tracks: []string{"microphone"}}, sink)
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	submit := make(chan error, 1)
	go func() {
		submit <- s.Submit(ctx, token, event("microphone", "waiting", 1, 0, time.Second, "Evidence", Final))
	}()
	select {
	case <-sink.entered:
	case <-time.After(time.Second):
		close(sink.release)
		t.Fatal("lookup did not begin")
	}
	pause := make(chan error, 1)
	go func() { pause <- s.Pause(context.Background()) }()
	waitForState(t, s, Paused)
	cancel()
	close(sink.release)
	if err := <-submit; !errors.Is(err, ErrEpoch) {
		t.Fatalf("invalidated lookup used obsolete cancellation authority: %v", err)
	}
	if err := <-pause; err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().State != Paused || len(sink.records) != 0 {
		t.Fatal("obsolete lookup changed state or persisted text")
	}
}

func TestContinuationStopDuringNoteLookupPreservesRetryRecoveryState(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled-control-%v", canceled), func(t *testing.T) {
			sink := &blockedLookupSink{indexedFixtureSink: &indexedFixtureSink{testSink: encryptedFixtureSink(), deleted: map[string]bool{}}, entered: make(chan struct{}), release: make(chan struct{})}
			sink.append = func(context.Context, Record) (Receipt, error) { return Receipt{}, errors.New("uncertain note write") }
			s, err := New(context.Background(), Config{SessionID: "meeting-1", Tracks: []string{"microphone"}}, sink)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			write := make(chan error, 1)
			go func() {
				write <- s.AddNote(context.Background(), ManualNoteInput{ID: "pending-note", Text: "Exact pending note"})
			}()
			<-sink.entered
			var stop chan error
			want := Stopped
			if canceled {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if err := s.Stop(ctx); !errors.Is(err, ErrCanceled) {
					t.Fatal(err)
				}
				want = Paused
			} else {
				stop = make(chan error, 1)
				go func() { stop <- s.Stop(context.Background()) }()
				waitForState(t, s, Stopping)
			}
			close(sink.release)
			if err := <-write; !errors.Is(err, ErrSink) {
				t.Fatal(err)
			}
			if stop != nil {
				if err := <-stop; !errors.Is(err, ErrSink) {
					t.Fatal(err)
				}
			}
			sink.append = nil
			if err := s.Retry(context.Background()); err != nil {
				t.Fatal(err)
			}
			if snap := s.Snapshot(); snap.State != want || len(snap.Notes) != 1 || snap.Pending != nil {
				t.Fatalf("note lookup lost control/recovery intent: want=%s got=%s", want, snap.State)
			}
		})
	}
}

func TestContinuationLookupReadFailureRetainsExactFinalForValidatedRetry(t *testing.T) {
	for _, oldAbsent := range []bool{false, true} {
		t.Run(fmt.Sprintf("old-absent-%v", oldAbsent), func(t *testing.T) {
			sink := &failedLookupSink{indexedFixtureSink: &indexedFixtureSink{testSink: encryptedFixtureSink(), deleted: map[string]bool{}}}
			s, err := New(context.Background(), Config{SessionID: "meeting-1", Tracks: []string{"microphone"}}, sink)
			if err != nil {
				t.Fatal(err)
			}
			token, err := s.Start(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if oldAbsent {
				if err := s.Submit(context.Background(), token, event("microphone", "later", 1, 10*time.Second, 11*time.Second, "Saved later evidence", Final)); err != nil {
					t.Fatal(err)
				}
			}
			sink.err = errors.New("temporary private disk lookup error")
			e := event("microphone", "pending", 1, 0, time.Second, "Exact pending lookup evidence", Final)
			if err := s.Submit(context.Background(), token, e); !errors.Is(err, ErrSink) {
				t.Fatal(err)
			}
			snap := s.Snapshot()
			if snap.State != Failed || snap.Pending == nil || snap.Pending.Event != e {
				t.Fatal("lookup failure dropped finalized input")
			}
			sink.err = nil
			err = s.Retry(context.Background())
			if oldAbsent {
				if !errors.Is(err, ErrOrder) || len(sink.records) != 1 {
					t.Fatalf("lookup retry bypassed ordering: records=%d err=%v", len(sink.records), err)
				}
			} else {
				if err != nil || len(sink.records) != 1 || s.Snapshot().State != Paused || s.Snapshot().Pending != nil {
					t.Fatalf("valid lookup retry failed: %v", err)
				}
			}
		})
	}
}

func TestContinuationLookupReadFailureRetainsExactReadyNote(t *testing.T) {
	sink := &failedLookupSink{indexedFixtureSink: &indexedFixtureSink{testSink: encryptedFixtureSink(), deleted: map[string]bool{}}, err: errors.New("temporary private disk lookup error")}
	s, err := New(context.Background(), Config{SessionID: "meeting-1", Tracks: []string{"microphone"}}, sink)
	if err != nil {
		t.Fatal(err)
	}
	note := ManualNoteInput{ID: "pending-note", RecordedAt: time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC), Text: "Exact ready agenda note"}
	if err := s.AddNote(context.Background(), note); !errors.Is(err, ErrSink) {
		t.Fatal(err)
	}
	if snap := s.Snapshot(); snap.Pending == nil || snap.Pending.Note != note {
		t.Fatal("lookup failure dropped authored note")
	}
	sink.err = nil
	if err := s.Retry(context.Background()); err != nil {
		t.Fatal(err)
	}
	if snap := s.Snapshot(); snap.State != Ready || snap.Pending != nil || len(snap.Notes) != 1 || snap.Notes[0].ManualNoteInput != note {
		t.Fatal("ready note lookup retry changed evidence/state")
	}
}
