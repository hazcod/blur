package meetings

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// This is a synthetic port fixture, not evidence that a production adapter
// encrypts. The engine is tested against the declared capability boundary.
type testSink struct {
	mu      sync.Mutex
	caps    SinkCapabilities
	records map[string]Record
	calls   []Record
	append  func(context.Context, Record) (Receipt, error)
}

func encryptedFixtureSink() *testSink {
	return &testSink{caps: SinkCapabilities{Encryption: AuthenticatedEncryption, AtomicIdempotent: true}, records: map[string]Record{}}
}

func (s *testSink) Capabilities() SinkCapabilities { return s.caps }
func (s *testSink) Append(ctx context.Context, record Record) (Receipt, error) {
	s.mu.Lock()
	s.calls = append(s.calls, record)
	appendFn := s.append
	s.mu.Unlock()
	if appendFn != nil {
		return appendFn(ctx, record)
	}
	return s.commit(record)
}

func (s *testSink) commit(record Record) (Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.records[record.Key]; ok && old != record {
		return Receipt{}, ErrConflict
	}
	s.records[record.Key] = record
	return Receipt{SourceID: "source-" + record.Key}, nil
}

func (s *testSink) writes() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Record(nil), s.calls...)
}

func newRunning(t *testing.T, limits Limits) (*Session, Token, *testSink) {
	t.Helper()
	sink := encryptedFixtureSink()
	s, err := New(context.Background(), Config{SessionID: "meeting-1", Tracks: []string{"microphone", "system_audio"}, Limits: limits}, sink)
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s, token, sink
}

func event(track, segment string, revision uint64, start, end time.Duration, text string, kind EventKind) Event {
	return Event{Kind: kind, SessionID: "meeting-1", TrackID: track, SegmentID: segment, Revision: revision, Start: start, End: end, Text: text}
}

func TestPartialReplacementStaysVolatileUntilFinal(t *testing.T) {
	s, token, sink := newRunning(t, Limits{})
	ctx := context.Background()
	first := event("microphone", "s1", 1, 0, time.Second, "Keep the", Partial)
	if err := s.Submit(ctx, token, first); err != nil {
		t.Fatal(err)
	}
	next := event("microphone", "s1", 2, 0, 2*time.Second, "Keep the source bytes.", Partial)
	if err := s.Submit(ctx, token, next); err != nil {
		t.Fatal(err)
	}
	before := s.Snapshot()
	if len(sink.writes()) != 0 || len(before.Finals) != 0 || len(before.Partials) != 1 || before.Partials[0] != next {
		t.Fatalf("partial text was persisted or failed to replace: %+v", before)
	}
	next.Kind = Final
	if err := s.Submit(ctx, token, next); err != nil {
		t.Fatal(err)
	}
	after := s.Snapshot()
	if len(sink.writes()) != 1 || len(after.Finals) != 1 || after.Finals[0].Event != next || after.Finals[0].SourceID == "" || len(after.Partials) != 0 {
		t.Fatalf("final source was not persisted once: %+v", after)
	}
	if err := s.Submit(ctx, token, next); err != nil || len(sink.writes()) != 1 {
		t.Fatalf("duplicate final was not idempotent: writes=%d err=%v", len(sink.writes()), err)
	}
}

func TestNewFailsClosedWithoutExplicitEncryptedIdempotentCapability(t *testing.T) {
	for _, caps := range []SinkCapabilities{{}, {Encryption: AuthenticatedEncryption}, {AtomicIdempotent: true}, {Encryption: "plaintext", AtomicIdempotent: true}} {
		sink := encryptedFixtureSink()
		sink.caps = caps
		if _, err := New(context.Background(), Config{SessionID: "meeting-1", Tracks: []string{"microphone"}}, sink); !errors.Is(err, ErrEncryption) || len(sink.writes()) != 0 {
			t.Errorf("undeclared or plaintext sink admitted: caps=%+v err=%v", caps, err)
		}
	}
}

func TestInterleavedTracksAndManualNotesHaveDistinctEvidence(t *testing.T) {
	s, token, sink := newRunning(t, Limits{})
	ctx := context.Background()
	for _, e := range []Event{
		event("microphone", "s1", 1, 0, 2*time.Second, "I will review the draft.", Final),
		event("system_audio", "s1", 1, 0, time.Second, "Maya owns the rollout.", Final),
		event("microphone", "s2", 1, 2*time.Second, 3*time.Second, "Remind me tomorrow.", Final),
	} {
		if err := s.Submit(ctx, token, e); err != nil {
			t.Fatal(err)
		}
	}
	note := ManualNoteInput{ID: "note-1", At: time.Second, Text: "Decision: preserve original source bytes.\n"}
	if err := s.AddNote(ctx, note); err != nil {
		t.Fatal(err)
	}
	got := s.Snapshot()
	if len(got.Finals) != 3 || len(got.Notes) != 1 || got.Notes[0].ManualNoteInput != note || got.Notes[0].SourceID == "" {
		t.Fatalf("track or manual-note provenance lost: %+v", got)
	}
	for i, record := range sink.writes() {
		if i < 3 && (record.Kind != TranscriptFinal || record.Note.Text != "") || i == 3 && (record.Kind != UserNote || record.Event.Text != "") {
			t.Fatalf("manual note and transcript evidence were merged: %+v", record)
		}
	}
}

func TestFinalizedEvidenceCannotBeRewritten(t *testing.T) {
	s, token, sink := newRunning(t, Limits{})
	e := event("microphone", "s1", 1, 0, time.Second, "Original final.", Final)
	if err := s.Submit(context.Background(), token, e); err != nil {
		t.Fatal(err)
	}
	e.Revision, e.Text = 2, "Changed final."
	if err := s.Submit(context.Background(), token, e); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting final accepted: %v", err)
	}
	got := s.Snapshot()
	if got.State != Failed || got.Issue != IssueConflict || len(sink.writes()) != 1 || got.Finals[0].Text != "Original final." {
		t.Fatalf("conflict was hidden or changed persisted evidence: %+v", got)
	}
}

func TestSnapshotsCannotMutateSession(t *testing.T) {
	s, token, _ := newRunning(t, Limits{})
	e := event("microphone", "s1", 1, 0, time.Second, "A partial.", Partial)
	if err := s.Submit(context.Background(), token, e); err != nil {
		t.Fatal(err)
	}
	first := s.Snapshot()
	first.Partials[0].Text = "Edited outside the engine"
	first.Partials = append(first.Partials, event("microphone", "extra", 1, time.Second, 2*time.Second, "Extra", Partial))
	second := s.Snapshot()
	if len(second.Partials) != 1 || second.Partials[0].Text != "A partial." {
		t.Fatalf("snapshot shared mutable aliases: %+v", second)
	}
}

func TestTranscriptBudgetCountsPartialReplacementAcrossTracks(t *testing.T) {
	s, token, sink := newRunning(t, Limits{MaxTranscriptBytes: 12, MaxEventBytes: 12})
	ctx := context.Background()
	if err := s.Submit(ctx, token, event("microphone", "s1", 1, 0, time.Second, "12345", Partial)); err != nil {
		t.Fatal(err)
	}
	if err := s.Submit(ctx, token, event("system_audio", "s1", 1, 0, time.Second, "abcdef", Partial)); err != nil {
		t.Fatal(err)
	}
	if err := s.Submit(ctx, token, event("microphone", "s1", 2, 0, 2*time.Second, strings.Repeat("x", 8), Partial)); !errors.Is(err, ErrLimit) {
		t.Fatalf("replacement bypassed aggregate byte budget: %v", err)
	}
	if got := s.Snapshot(); len(got.Partials) != 2 || len(sink.writes()) != 0 || len(got.Partials[0].Text)+len(got.Partials[1].Text) != 11 {
		t.Fatalf("over-limit replacement discarded accepted text: %+v", got)
	}
}

func TestPauseResumeAndStopRejectExpiredCallbacks(t *testing.T) {
	s, token, sink := newRunning(t, Limits{})
	ctx := context.Background()
	e := event("microphone", "s1", 1, 0, time.Second, "Unstable words", Partial)
	if err := s.Submit(ctx, token, e); err != nil {
		t.Fatal(err)
	}
	if err := s.Pause(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Submit(ctx, token, e); !errors.Is(err, ErrEpoch) {
		t.Fatalf("paused callback admitted: %v", err)
	}
	if got := s.Snapshot(); got.State != Paused || len(got.Partials) != 0 || len(sink.writes()) != 0 {
		t.Fatalf("paused volatile words looked durable: %+v", got)
	}
	next, err := s.Resume(ctx)
	if err != nil || next.Epoch <= token.Epoch {
		t.Fatalf("resume reused callback epoch: token=%+v err=%v", next, err)
	}
	e.Kind = Final
	if err := s.Submit(ctx, token, e); !errors.Is(err, ErrEpoch) {
		t.Fatalf("old callback from before resume admitted: %v", err)
	}
	if err := s.Submit(ctx, next, e); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Submit(ctx, next, e); !errors.Is(err, ErrEpoch) {
		t.Fatalf("stopped callback admitted: %v", err)
	}
	if err := s.Stop(ctx); err != nil {
		t.Fatalf("stop retry not idempotent: %v", err)
	}
	if _, err := s.Resume(ctx); !errors.Is(err, ErrState) {
		t.Fatalf("stopped session resumed: %v", err)
	}
}

func TestSinkFailureRetainsPendingEvidenceAndRetryUsesStableIdempotencyKey(t *testing.T) {
	s, token, sink := newRunning(t, Limits{})
	first := true
	sink.append = func(_ context.Context, r Record) (Receipt, error) {
		receipt, err := sink.commit(r)
		if first {
			first = false
			return Receipt{}, errors.New("private meeting words from the sink must not escape")
		}
		return receipt, err
	}
	e := event("microphone", "s1", 1, 0, time.Second, "The accepted final.", Final)
	err := s.Submit(context.Background(), token, e)
	if !errors.Is(err, ErrSink) || strings.Contains(err.Error(), "private meeting words") {
		t.Fatalf("sink failure hidden or disclosed source content: %v", err)
	}
	failed := s.Snapshot()
	if failed.State != Failed || failed.Issue != IssueSink || failed.Pending == nil || failed.Pending.Event != e || len(failed.Finals) != 0 {
		t.Fatalf("uncertain append was published or lost: %+v", failed)
	}
	failed.Pending.Event.Text = "Mutation outside engine"
	if err := s.Retry(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := s.Snapshot()
	writes := sink.writes()
	if got.State != Paused || got.Issue != NoIssue || got.Pending != nil || len(got.Finals) != 1 || got.Finals[0].Event != e || len(writes) != 2 || writes[0] != writes[1] || len(sink.records) != 1 {
		t.Fatalf("retry duplicated or changed uncertain evidence: snapshot=%+v writes=%+v", got, writes)
	}
}

func TestManualNoteFailurePreservesUnsavedNotesSeparately(t *testing.T) {
	s, _, sink := newRunning(t, Limits{})
	sink.append = func(context.Context, Record) (Receipt, error) { return Receipt{}, errors.New("storage unavailable") }
	note := ManualNoteInput{ID: "note-1", At: time.Second, Text: "My own decision notes.\n"}
	if err := s.AddNote(context.Background(), note); !errors.Is(err, ErrSink) {
		t.Fatalf("manual write failure hidden: %v", err)
	}
	got := s.Snapshot()
	if got.State != Failed || got.Pending == nil || got.Pending.Kind != UserNote || got.Pending.Note != note || len(got.Notes) != 0 || len(got.Finals) != 0 {
		t.Fatalf("unsaved authored note was lost or misattributed: %+v", got)
	}
	sink.append = nil
	if err := s.Retry(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := s.Snapshot(); len(got.Notes) != 1 || got.Notes[0].ManualNoteInput != note || len(got.Finals) != 0 {
		t.Fatalf("manual note retry changed provenance: %+v", got)
	}
}

func waitForState(t *testing.T, s *Session, want State) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if s.Snapshot().State == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("session did not enter %s, got %+v", want, s.Snapshot())
}

func TestControlsInvalidateEpochBeforeDrainingAdmittedFinal(t *testing.T) {
	for _, stopping := range []bool{false, true} {
		t.Run(map[bool]string{false: "pause", true: "stop"}[stopping], func(t *testing.T) {
			s, token, sink := newRunning(t, Limits{})
			started, release := make(chan struct{}), make(chan struct{})
			sink.append = func(_ context.Context, r Record) (Receipt, error) {
				close(started)
				<-release
				return sink.commit(r)
			}
			write := make(chan error, 1)
			go func() {
				write <- s.Submit(context.Background(), token, event("microphone", "s1", 1, 0, time.Second, "Admitted final", Final))
			}()
			<-started
			control := make(chan error, 1)
			go func() {
				if stopping {
					control <- s.Stop(context.Background())
				} else {
					control <- s.Pause(context.Background())
				}
			}()
			want := Paused
			if stopping {
				want = Stopping
			}
			waitForState(t, s, want)
			if err := s.Submit(context.Background(), token, event("system_audio", "s2", 1, 0, time.Second, "Late callback", Final)); !errors.Is(err, ErrEpoch) {
				t.Errorf("late callback admitted while control was draining: %v", err)
			}
			select {
			case err := <-control:
				t.Errorf("control returned before admitted write drained: %v", err)
			default:
			}
			close(release)
			if err := <-write; err != nil {
				t.Fatal(err)
			}
			if err := <-control; err != nil {
				t.Fatal(err)
			}
			got := s.Snapshot()
			if len(got.Finals) != 1 || len(sink.writes()) != 1 || stopping && got.State != Stopped || !stopping && got.State != Paused {
				t.Fatalf("drain lost admitted final or accepted late text: %+v", got)
			}
		})
	}
}

func TestCanceledContextFailsVisiblyAndDoesNotPersist(t *testing.T) {
	s, token, sink := newRunning(t, Limits{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.Submit(ctx, token, event("microphone", "s1", 1, 0, time.Second, "Private words", Final))
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrCanceled) || len(sink.writes()) != 0 {
		t.Fatalf("canceled submission persisted or hid cancellation: err=%v writes=%d", err, len(sink.writes()))
	}
	if got := s.Snapshot(); got.State != Failed || got.Issue != IssueCanceled {
		t.Fatalf("canceled input still appeared to record: %+v", got)
	}
}

func TestCancellationAfterSinkAcceptanceDoesNotPublishAsSuccess(t *testing.T) {
	s, token, sink := newRunning(t, Limits{})
	ctx, cancel := context.WithCancel(context.Background())
	sink.append = func(_ context.Context, record Record) (Receipt, error) {
		receipt, err := sink.commit(record)
		cancel()
		return receipt, err
	}
	err := s.Submit(ctx, token, event("microphone", "s1", 1, 0, time.Second, "Private final", Final))
	got := s.Snapshot()
	if !errors.Is(err, context.Canceled) || got.State != Failed || got.Pending == nil || len(got.Finals) != 0 {
		t.Fatalf("late success escaped cancellation: snapshot=%+v err=%v", got, err)
	}
}

func TestInputAndRetentionBoundsRejectBeforeSink(t *testing.T) {
	for _, change := range []func(*Event){
		func(e *Event) { e.SessionID = "another-session" },
		func(e *Event) { e.TrackID = "unconfigured" },
		func(e *Event) { e.SegmentID = "../a path" },
		func(e *Event) { e.Revision = 0 },
		func(e *Event) { e.Start = -1 },
		func(e *Event) { e.End = -1 },
		func(e *Event) { e.Text = "\x00private" },
		func(e *Event) { e.Text = "\xff" },
		func(e *Event) { e.Text = "  " },
		func(e *Event) { e.Text = strings.Repeat("x", (16<<10)+1) },
		func(e *Event) { e.Kind = "model_instruction" },
	} {
		s, token, sink := newRunning(t, Limits{})
		e := event("microphone", "s1", 1, 0, time.Second, "Valid final", Final)
		change(&e)
		if err := s.Submit(context.Background(), token, e); err == nil || len(sink.writes()) != 0 {
			t.Errorf("invalid event reached encrypted sink: event=%+v err=%v", e, err)
		}
	}
	s, token, sink := newRunning(t, Limits{MaxSegments: 2})
	for i, id := range []string{"s1", "s2"} {
		if err := s.Submit(context.Background(), token, event("microphone", id, 1, time.Duration(i)*time.Second, time.Duration(i+1)*time.Second, "Bounded final", Final)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Submit(context.Background(), token, event("microphone", "s3", 1, 2*time.Second, 3*time.Second, "Excess final", Final)); !errors.Is(err, ErrLimit) || len(sink.writes()) != 2 || len(s.Snapshot().Finals) != 2 {
		t.Fatalf("retention limit grew or evicted conflict metadata: writes=%d err=%v", len(sink.writes()), err)
	}
}

func TestRevisionAndTrackTimesRemainMonotonic(t *testing.T) {
	s, token, sink := newRunning(t, Limits{})
	first := event("microphone", "s1", 2, 0, 2*time.Second, "Version two", Partial)
	if err := s.Submit(context.Background(), token, first); err != nil {
		t.Fatal(err)
	}
	older := first
	older.Revision, older.Text = 1, "Older words"
	if err := s.Submit(context.Background(), token, older); !errors.Is(err, ErrOrder) {
		t.Fatalf("older partial replaced current version: %v", err)
	}
	first.Kind = Final
	if err := s.Submit(context.Background(), token, first); err != nil {
		t.Fatal(err)
	}
	if err := s.Submit(context.Background(), token, event("microphone", "s2", 1, time.Second, 3*time.Second, "Overlaps saved span", Final)); !errors.Is(err, ErrOrder) || len(sink.writes()) != 1 {
		t.Fatalf("track timestamps moved backwards: writes=%d err=%v", len(sink.writes()), err)
	}
}

func TestManualNoteRetryRemainsIdempotentAtRetentionLimit(t *testing.T) {
	s, _, sink := newRunning(t, Limits{MaxNotes: 1, MaxManualBytes: 9})
	note := ManualNoteInput{ID: "note-1", At: time.Second, Text: "My notes."}
	if err := s.AddNote(context.Background(), note); err != nil {
		t.Fatal(err)
	}
	if err := s.AddNote(context.Background(), note); err != nil || len(sink.writes()) != 1 || len(s.Snapshot().Notes) != 1 {
		t.Fatalf("idempotent note retry consumed retention budget: writes=%d err=%v", len(sink.writes()), err)
	}
}

func TestWrappedSinkCancellationDoesNotExposeSourceContents(t *testing.T) {
	s, token, sink := newRunning(t, Limits{})
	sink.append = func(context.Context, Record) (Receipt, error) {
		return Receipt{}, fmt.Errorf("confidential meeting words: %w", context.Canceled)
	}
	err := s.Submit(context.Background(), token, event("microphone", "s1", 1, 0, time.Second, "Final", Final))
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrCanceled) || strings.Contains(err.Error(), "confidential") {
		t.Fatalf("sink cancellation exposed source data or hid failure: %v", err)
	}
}

func TestRevokedEncryptionCapabilityFailsBeforeAppend(t *testing.T) {
	s, token, sink := newRunning(t, Limits{})
	sink.caps = SinkCapabilities{}
	err := s.Submit(context.Background(), token, event("microphone", "s1", 1, 0, time.Second, "Final", Final))
	got := s.Snapshot()
	if !errors.Is(err, ErrEncryption) || got.State != Failed || got.Pending == nil || len(sink.writes()) != 0 {
		t.Fatalf("revoked capability still wrote text: snapshot=%+v writes=%d err=%v", got, len(sink.writes()), err)
	}
}

func TestRecordingStateErrorsDoNotPretendToStartCapture(t *testing.T) {
	sink := encryptedFixtureSink()
	s, err := New(context.Background(), Config{SessionID: "meeting-1", Tracks: []string{"microphone"}}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().State != Ready {
		t.Fatal("construction automatically started the session")
	}
	if err := s.Pause(context.Background()); !errors.Is(err, ErrState) {
		t.Fatalf("pause before explicit start appeared successful: %v", err)
	}
	if _, err := s.Resume(context.Background()); !errors.Is(err, ErrState) {
		t.Fatalf("resume before explicit start appeared successful: %v", err)
	}
	if _, err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(context.Background()); !errors.Is(err, ErrState) {
		t.Fatalf("duplicate start appeared successful: %v", err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(context.Background()); !errors.Is(err, ErrState) {
		t.Fatalf("stopped session silently restarted: %v", err)
	}
	if len(sink.writes()) != 0 {
		t.Fatal("recording controls wrote transcript without events")
	}
}

func TestPendingFinalDoesNotRetainReplacedPartialBeyondBudget(t *testing.T) {
	s, token, sink := newRunning(t, Limits{MaxTranscriptBytes: 12, MaxEventBytes: 12})
	if err := s.Submit(context.Background(), token, event("microphone", "s1", 1, 0, time.Second, "12345678", Partial)); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	sink.append = func(_ context.Context, r Record) (Receipt, error) {
		close(started)
		<-release
		return sink.commit(r)
	}
	done := make(chan error, 1)
	go func() {
		done <- s.Submit(context.Background(), token, event("microphone", "s1", 2, 0, 2*time.Second, "1234567890", Final))
	}()
	<-started
	got := s.Snapshot()
	bytes := 0
	for _, e := range got.Partials {
		bytes += len(e.Text)
	}
	if got.Pending != nil {
		bytes += len(got.Pending.Event.Text)
	}
	if bytes > 12 {
		t.Errorf("pending final retained its superseded partial: retained=%d want<=12", bytes)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestThousandsOfShortFinalsUseIncrementalBoundedWrites(t *testing.T) {
	const count = 10000
	s, token, sink := newRunning(t, Limits{MaxSegments: count})
	ctx := context.Background()
	for i := 0; i < count; i++ {
		start := time.Duration(i) * 2 * time.Second
		e := event("microphone", "s"+strconv.Itoa(i), 1, start, start+time.Second, "Short final words.", Final)
		if err := s.Submit(ctx, token, e); err != nil {
			t.Fatalf("normal short-segment stream failed at %d of %d: %v", i, count, err)
		}
	}
	writes := sink.writes()
	if len(writes) != count {
		t.Fatalf("incremental stream made %d writes, want %d", len(writes), count)
	}
	for _, record := range writes {
		if record.Kind != TranscriptFinal || record.Event.Text != "Short final words." || record.Note.Text != "" {
			t.Fatal("sink received a rewritten full transcript instead of one final segment")
		}
	}
	// Snapshot once, not once per event: UI integration must page/preview a long
	// meeting rather than copy the complete historical snapshot every tick.
	got := s.Snapshot()
	if len(got.Finals) != count || got.State != Running {
		t.Fatalf("short segment history was lost: finals=%d state=%s", len(got.Finals), got.State)
	}
	start := time.Duration(count) * 2 * time.Second
	err := s.Submit(ctx, token, event("microphone", "overflow", 1, start, start+time.Second, "One beyond the cap", Final))
	if !errors.Is(err, ErrLimit) || len(sink.writes()) != count || len(s.Snapshot().Finals) != count {
		t.Fatalf("explicit segment envelope failed: writes=%d err=%v", len(sink.writes()), err)
	}
}

func TestNativeMaximumTextEventFitsEngineEnvelope(t *testing.T) {
	s, token, sink := newRunning(t, Limits{})
	e := event("system_audio", "s1", 1, 0, time.Second, strings.Repeat("x", 16<<10), Final)
	if err := s.Submit(context.Background(), token, e); err != nil || len(sink.writes()) != 1 {
		t.Fatalf("native 16KiB final did not fit engine contract: writes=%d err=%v", len(sink.writes()), err)
	}
}

func TestZeroValueSessionRejectsWithoutWaitingForContext(t *testing.T) {
	var s Session
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := s.Start(ctx); !errors.Is(err, ErrState) {
		t.Fatalf("unconstructed session waited or pretended to start: %v", err)
	}
	if got := s.Snapshot(); got.State != Failed {
		t.Fatalf("unconstructed session reported usable state: %+v", got)
	}
}
