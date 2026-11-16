package meetings

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// The wrapper observes entry into a cancellable wait without replacing the
// session, its gate, or its context cancellation behavior.
type observedWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *observedWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestQueuedResumeCannotOvertakePauseWhileFinalDrains(t *testing.T) {
	s, token, sink := newRunning(t, Limits{})
	started, release := make(chan struct{}), make(chan struct{})
	sink.append = func(_ context.Context, record Record) (Receipt, error) {
		close(started)
		<-release
		return sink.commit(record)
	}
	write := make(chan error, 1)
	go func() {
		write <- s.Submit(context.Background(), token, event("microphone", "s1", 1, 0, time.Second, "Final before pause", Final))
	}()
	<-started
	resume := make(chan error, 1)
	go func() {
		_, err := s.Resume(context.Background())
		resume <- err
	}()
	var resumeErr error
	resumeReturned := false
	select {
	case resumeErr = <-resume:
		resumeReturned = true
	case <-time.After(time.Second):
		t.Error("Resume while Running queued behind persistence instead of rejecting the state")
	}
	pause := make(chan error, 1)
	go func() { pause <- s.Pause(context.Background()) }()
	waitForState(t, s, Paused)
	close(release)
	if err := <-write; err != nil {
		t.Fatal(err)
	}
	if !resumeReturned {
		resumeErr = <-resume
	}
	if !errors.Is(resumeErr, ErrState) {
		t.Errorf("queued Resume adopted a state that changed after its request: %v", resumeErr)
	}
	if err := <-pause; err != nil || s.Snapshot().State != Paused {
		t.Fatalf("Pause returned success while session was not paused: state=%s err=%v", s.Snapshot().State, err)
	}
}

func TestResumeCannotEnterRunningDuringPauseTransition(t *testing.T) {
	s, token, sink := newRunning(t, Limits{})
	started, release := make(chan struct{}), make(chan struct{})
	sink.append = func(_ context.Context, record Record) (Receipt, error) {
		close(started)
		<-release
		return sink.commit(record)
	}
	write := make(chan error, 1)
	go func() {
		write <- s.Submit(context.Background(), token, event("microphone", "s1", 1, 0, time.Second, "Final before pause", Final))
	}()
	<-started
	pause := make(chan error, 1)
	go func() { pause <- s.Pause(context.Background()) }()
	waitForState(t, s, Paused)
	resume := make(chan error, 1)
	go func() {
		_, err := s.Resume(context.Background())
		resume <- err
	}()
	var resumeErr error
	resumeReturned := false
	select {
	case resumeErr = <-resume:
		resumeReturned = true
	case <-time.After(time.Second):
		t.Error("Resume entered the queue while Pause still owned its transition")
	}
	close(release)
	if err := <-write; err != nil {
		t.Fatal(err)
	}
	if err := <-pause; err != nil {
		t.Fatal(err)
	}
	if !resumeReturned {
		resumeErr = <-resume
	}
	if !errors.Is(resumeErr, ErrState) || s.Snapshot().State != Paused {
		t.Fatalf("overlapping Resume undid the pending Pause: state=%s err=%v", s.Snapshot().State, resumeErr)
	}
}

func TestManualNoteRetryRestoresOriginalNonRecordingState(t *testing.T) {
	for _, before := range []State{Ready, Running, Paused, Stopped} {
		t.Run(string(before), func(t *testing.T) {
			sink := encryptedFixtureSink()
			s, err := New(context.Background(), Config{SessionID: "meeting-1", Tracks: []string{"microphone"}}, sink)
			if err != nil {
				t.Fatal(err)
			}
			if before != Ready {
				if _, err := s.Start(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if before == Paused {
				if err := s.Pause(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if before == Stopped {
				if err := s.Stop(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			sink.append = func(context.Context, Record) (Receipt, error) { return Receipt{}, errors.New("sink unavailable") }
			if err := s.AddNote(context.Background(), ManualNoteInput{ID: "note-1", Text: "Separate agenda notes"}); !errors.Is(err, ErrSink) {
				t.Fatalf("note failure was not visible: %v", err)
			}
			sink.append = nil
			if err := s.Retry(context.Background()); err != nil {
				t.Fatal(err)
			}
			want := before
			if before == Running {
				want = Paused
			}
			if got := s.Snapshot(); got.State != want || len(got.Notes) != 1 || got.Pending != nil {
				t.Fatalf("note retry changed recording authority: before=%s want=%s got=%+v", before, want, got)
			}
			if before == Stopped {
				if _, err := s.Resume(context.Background()); !errors.Is(err, ErrState) {
					t.Fatalf("retry reopened a stopped session: %v", err)
				}
			}
			if before == Ready {
				if _, err := s.Start(context.Background()); err != nil {
					t.Fatalf("agenda-note retry removed explicit initial Start: %v", err)
				}
			}
		})
	}
}

func TestStaleCallbackCancellationCannotFailResumedEpoch(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "live context", true: "canceled context"}[canceled], func(t *testing.T) {
			s, stale, sink := newRunning(t, Limits{})
			if err := s.Pause(context.Background()); err != nil {
				t.Fatal(err)
			}
			current, err := s.Resume(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if canceled {
				cancel()
			}
			err = s.Submit(ctx, stale, event("microphone", "stale", 1, 0, time.Second, "Old epoch words", Final))
			if !errors.Is(err, ErrEpoch) || s.Snapshot().State != Running || s.Snapshot().Epoch != current.Epoch || len(sink.writes()) != 0 {
				t.Fatalf("stale cancellation changed current recording authority: state=%s epoch=%d want=%d writes=%d err=%v", s.Snapshot().State, s.Snapshot().Epoch, current.Epoch, len(sink.writes()), err)
			}
			if err := s.Submit(context.Background(), current, event("microphone", "current", 1, 0, time.Second, "Current epoch words", Final)); err != nil || len(sink.writes()) != 1 {
				t.Fatalf("current callback was poisoned by stale cancellation: %v", err)
			}
		})
	}
}

func TestQueuedCallbackCancellationCannotFailPauseTransition(t *testing.T) {
	s, token, sink := newRunning(t, Limits{})
	started, release := make(chan struct{}), make(chan struct{})
	sink.append = func(_ context.Context, record Record) (Receipt, error) {
		close(started)
		<-release
		return sink.commit(record)
	}
	write := make(chan error, 1)
	go func() {
		write <- s.Submit(context.Background(), token, event("microphone", "admitted", 1, 0, time.Second, "Admitted words", Final))
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiting := &observedWaitContext{Context: ctx, waiting: make(chan struct{})}
	queued := make(chan error, 1)
	go func() {
		queued <- s.Submit(waiting, token, event("system_audio", "queued", 1, 0, time.Second, "Never admitted words", Final))
	}()
	<-waiting.waiting
	pause := make(chan error, 1)
	go func() { pause <- s.Pause(context.Background()) }()
	waitForState(t, s, Paused)
	cancel()
	if err := <-queued; !errors.Is(err, ErrEpoch) || s.Snapshot().State != Paused {
		t.Errorf("obsolete waiter cancellation failed the pause: state=%s err=%v", s.Snapshot().State, err)
	}
	close(release)
	if err := <-write; err != nil {
		t.Fatal(err)
	}
	if err := <-pause; err != nil {
		t.Fatalf("late cancellation broke transition drainage: %v", err)
	}
	if _, err := s.Resume(context.Background()); err != nil || len(sink.writes()) != 1 {
		t.Fatalf("current session was poisoned by obsolete waiter: writes=%d err=%v", len(sink.writes()), err)
	}
}

func TestStopDuringFailedAppendKeepsRetryTerminal(t *testing.T) {
	s, token, sink := newRunning(t, Limits{})
	started, release := make(chan struct{}), make(chan struct{})
	sink.append = func(context.Context, Record) (Receipt, error) {
		close(started)
		<-release
		return Receipt{}, errors.New("sink unavailable")
	}
	write := make(chan error, 1)
	go func() {
		write <- s.Submit(context.Background(), token, event("microphone", "s1", 1, 0, time.Second, "Final before stop", Final))
	}()
	<-started
	stop := make(chan error, 1)
	go func() { stop <- s.Stop(context.Background()) }()
	waitForState(t, s, Stopping)
	close(release)
	if err := <-write; !errors.Is(err, ErrSink) {
		t.Fatalf("append failure not reported: %v", err)
	}
	if err := <-stop; !errors.Is(err, ErrSink) {
		t.Fatalf("stop pretended an unsuccessful append was durable: %v", err)
	}
	sink.append = nil
	if err := s.Retry(context.Background()); err != nil || s.Snapshot().State != Stopped {
		t.Fatalf("retry discarded the accepted Stop intent: state=%s err=%v", s.Snapshot().State, err)
	}
	if _, err := s.Resume(context.Background()); !errors.Is(err, ErrState) {
		t.Fatalf("retry reopened stopped recording: %v", err)
	}
}
