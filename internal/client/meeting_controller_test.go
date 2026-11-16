package client

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/platform/darwin"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

// These tests exercise the public Client facade and real encrypted repository;
// the only substitute is the audio device/driver boundary.
type controllerAudioFixture struct {
	mu      sync.Mutex
	stream  *controllerAudioStream
	config  darwin.MeetingAudioStart
	onStart func(context.Context)
	closed  atomic.Bool
}

func (a *controllerAudioFixture) Probe(context.Context, string) (darwin.MeetingAudioAvailability, error) {
	return darwin.MeetingAudioAvailability{SpeechAvailable: true, RequestedLocaleInstalled: true, MicrophonePermission: "authorized", SystemPermission: true, InstalledLocales: []string{"en-US"}}, nil
}
func (*controllerAudioFixture) RequestAccess(context.Context, []string) error { return nil }
func (*controllerAudioFixture) InstallLocale(context.Context, string) error   { return nil }
func (a *controllerAudioFixture) StartMeeting(ctx context.Context, config darwin.MeetingAudioStart) (meetingAudioStream, error) {
	a.mu.Lock()
	a.config = config
	a.stream.id = config.SessionID
	hook := a.onStart
	a.mu.Unlock()
	if hook != nil {
		hook(ctx)
	}
	return a.stream, nil
}
func (a *controllerAudioFixture) Close() error { a.closed.Store(true); return nil }

type controllerAudioControl struct {
	operation string
	epoch     uint64
}
type controllerAudioStream struct {
	id           string
	batches      chan []darwin.MeetingAudioEvent
	controls     chan controllerAudioControl
	closed       chan struct{}
	closeOnce    sync.Once
	autoComplete atomic.Bool
}

func (s *controllerAudioStream) Poll(ctx context.Context) ([]darwin.MeetingAudioEvent, error) {
	select {
	case events := <-s.batches:
		return events, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.closed:
		return nil, darwin.ErrMeetingAudioUnavailable
	}
}
func (s *controllerAudioStream) Control(operation string, epoch uint64) error {
	select {
	case <-s.closed:
		return darwin.ErrMeetingAudioUnavailable
	default:
	}
	if epoch == 0 {
		return darwin.ErrMeetingAudioInvalid
	}
	s.controls <- controllerAudioControl{operation, epoch}
	if operation == "stop" && s.autoComplete.Load() {
		s.emit(controllerState(s.id, epoch+1, "completed"))
	}
	return nil
}
func (s *controllerAudioStream) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}
func (s *controllerAudioStream) emit(events ...darwin.MeetingAudioEvent) { s.batches <- events }

type controllerFixture struct {
	c     *Client
	store *durable.Store
	audio *controllerAudioFixture
	now   time.Time
}

func newControllerFixture(t *testing.T) controllerFixture {
	t.Helper()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	s := &controllerAudioStream{batches: make(chan []darwin.MeetingAudioEvent, 64), controls: make(chan controllerAudioControl, 32), closed: make(chan struct{})}
	s.autoComplete.Store(true)
	audio := &controllerAudioFixture{stream: s}
	c := NewClient(store)
	c.meetings.open = func() (meetingAudio, error) { return audio, nil }
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	c.meetings.now = func() time.Time { return now }
	t.Cleanup(func() { c.Close(); _ = store.Close() })
	return controllerFixture{c: c, store: store, audio: audio, now: now}
}
func (f controllerFixture) start(t *testing.T) string {
	t.Helper()
	if err := f.c.StartMeeting(context.Background(), "Launch review", "en-US", []string{"microphone"}); err != nil {
		t.Fatal(err)
	}
	return f.c.MeetingLive().Meeting.ID
}
func controllerState(id string, epoch uint64, state string) darwin.MeetingAudioEvent {
	return darwin.MeetingAudioEvent{Version: 1, SessionID: id, Epoch: epoch, Kind: "state", State: state}
}
func controllerWords(id string, epoch uint64, segment, text string, final bool) darwin.MeetingAudioEvent {
	start, end := int64(0), int64(1000)
	kind := "preview"
	if final {
		kind = "transcript"
	}
	return darwin.MeetingAudioEvent{Version: 1, SessionID: id, Epoch: epoch, Kind: kind, Track: "microphone", SegmentID: segment, Revision: 1, StartMS: &start, EndMS: &end, Final: &final, Text: text}
}
func waitController(t *testing.T, c *Client, predicate func(MeetingLiveView) bool) MeetingLiveView {
	t.Helper()
	// Real encrypted writes under the race detector are deliberately retained;
	// give bounded multi-record drains time without replacing persistence.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		v := c.MeetingLive()
		if predicate(v) {
			return v
		}
		time.Sleep(time.Millisecond)
	}
	v := c.MeetingLive()
	t.Fatalf("controller did not reach expected state: state=%s issue=%s rows=%d", v.State, v.Issue, len(v.Tail))
	return v
}
func controllerControl(t *testing.T, s *controllerAudioStream) controllerAudioControl {
	t.Helper()
	select {
	case command := <-s.controls:
		return command
	case <-time.After(4 * time.Second):
		t.Fatal("native control was not sent")
		return controllerAudioControl{}
	}
}
func controllerRecords(t *testing.T, f controllerFixture, id string) []meetings.StoredRecord {
	t.Helper()
	var records []meetings.StoredRecord
	for cursor, pass := uint64(0), 0; pass < 16; pass++ {
		page, err := f.store.MeetingRecords(context.Background(), id, meetings.PageRequest{After: cursor, Limit: 50, MaxBytes: 256 << 10})
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, page.Records...)
		if !page.More {
			return records
		}
		cursor = page.Next
	}
	t.Fatal("fixture record scan exceeded bounded pages")
	return nil
}

func TestMeetingControllerRequiresStartAcknowledgementAndPersistsEarlyFinal(t *testing.T) {
	f := newControllerFixture(t)
	id := f.start(t)
	if v := f.c.MeetingLive(); v.State != "starting" || v.Meeting.State != meetings.Ready || v.AudioRecording {
		t.Fatalf("recording claimed before native acknowledgement: %+v", v)
	}
	f.audio.stream.emit(controllerState(id, 1, "starting"), controllerWords(id, 1, "early", "Mira owns rollback.", true))
	v := waitController(t, f.c, func(v MeetingLiveView) bool { return len(v.Tail) == 1 })
	if v.State != "starting" || v.Meeting.State != meetings.Running || v.AudioRecording {
		t.Fatalf("early final changed recording claim or failed durable admission: %+v", v)
	}
	if rows := controllerRecords(t, f, id); len(rows) != 1 || rows[0].Record.Event.Text != "Mira owns rollback." {
		t.Fatalf("early final not saved: %+v", rows)
	}
	f.audio.stream.emit(controllerState(id, 1, "running"), controllerWords(id, 1, "moving", "PRIVATE VOLATILE PREVIEW", false))
	v = waitController(t, f.c, func(v MeetingLiveView) bool { return v.Preview["microphone"] != "" })
	if !v.AudioRecording {
		t.Fatal("native running acknowledgement did not expose actual audio recording")
	}
	if len(controllerRecords(t, f, id)) != 1 {
		t.Fatal("volatile preview became durable evidence")
	}
	v.Preview["microphone"] = "mutated snapshot"
	v.Tail[0].Record.Event.Text = "mutated snapshot"
	if live := f.c.MeetingLive(); live.Preview["microphone"] != "PRIVATE VOLATILE PREVIEW" || live.Tail[0].Record.Event.Text != "Mira owns rollback." {
		t.Fatal("caller can mutate controller snapshot")
	}
}

func TestMeetingControllerUnavailableAudioKeepsTypedNotesUsable(t *testing.T) {
	f := newControllerFixture(t)
	var opens atomic.Int64
	f.c.meetings.open = func() (meetingAudio, error) { opens.Add(1); return nil, darwin.ErrMeetingAudioUnavailable }
	id, err := f.c.NewMeetingNotes(context.Background(), "Offline agenda", "en-US", []string{"microphone"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.c.AddMeetingNote(context.Background(), id, meetings.ManualNoteInput{ID: "offline-note", Text: "Agenda before device access"}); err != nil {
		t.Fatal(err)
	}
	if opens.Load() != 0 {
		t.Fatal("typed notes opened an audio device")
	}
	if err := f.c.StartMeeting(context.Background(), "Ignored title", "en-US", []string{"microphone"}); !errors.Is(err, darwin.ErrMeetingAudioUnavailable) {
		t.Fatalf("unavailable driver claimed successful recording: %v", err)
	}
	if v := f.c.MeetingLive(); v.State != "ready" || v.Meeting.ID != id {
		t.Fatalf("failed start replaced saved notes or claimed recording: %+v", v)
	}
	if rows := controllerRecords(t, f, id); len(rows) != 1 || rows[0].Record.Note.Text != "Agenda before device access" {
		t.Fatalf("unavailable driver erased typed notes: %+v", rows)
	}
	f.c.meetings.open = func() (meetingAudio, error) { return f.audio, nil }
	if got := f.start(t); got != id {
		t.Fatalf("retry replaced typed agenda: %s != %s", got, id)
	}
}

func TestMeetingControllerLevelsExpireAndPauseResumeRejectsStaleEpochs(t *testing.T) {
	f := newControllerFixture(t)
	id := f.start(t)
	level := 0.72
	f.audio.stream.emit(controllerState(id, 1, "starting"), controllerState(id, 1, "running"), darwin.MeetingAudioEvent{Version: 1, SessionID: id, Epoch: 1, Kind: "level", Track: "microphone", Level: &level})
	v := waitController(t, f.c, func(v MeetingLiveView) bool { return v.AudioLevel(f.now) == level })
	if v.AudioLevel(f.now.Add(501*time.Millisecond)) != 0 || v.AudioLevel(f.now.Add(-time.Nanosecond)) != 0 || len(controllerRecords(t, f, id)) != 0 {
		t.Fatal("meter fabricated a fresh value or persisted audio levels")
	}
	if err := f.c.ControlMeeting(context.Background(), "pause"); err != nil {
		t.Fatal(err)
	}
	if got := controllerControl(t, f.audio.stream); got != (controllerAudioControl{"pause", 1}) {
		t.Fatal(got)
	}
	f.audio.stream.emit(controllerState(id, 2, "pausing"), controllerState(id, 2, "paused"), controllerWords(id, 1, "late-pause", "must not be saved", true))
	v = waitController(t, f.c, func(v MeetingLiveView) bool { return v.State == "paused" })
	if v.AudioLevel(f.now) != 0 || len(v.Levels) != 0 || len(v.Preview) != 0 || v.AudioRecording {
		t.Fatal("pause retained volatile recording indicators")
	}
	if err := f.c.ControlMeeting(context.Background(), "resume"); err != nil {
		t.Fatal(err)
	}
	if got := controllerControl(t, f.audio.stream); got != (controllerAudioControl{"resume", 2}) {
		t.Fatal(got)
	}
	f.audio.stream.emit(controllerState(id, 3, "starting"), controllerWords(id, 1, "late-resume", "must not be saved either", true), controllerWords(id, 3, "resumed", "Resume final before running ack", true), controllerState(id, 3, "running"))
	v = waitController(t, f.c, func(v MeetingLiveView) bool { return v.State == "running" && len(v.Tail) == 1 })
	if rows := controllerRecords(t, f, id); len(rows) != 1 || rows[0].Record.Event.Text != "Resume final before running ack" || v.AudioLevel(f.now) != 0 {
		t.Fatalf("resume did not fence stale evidence/levels: %+v", rows)
	}
}

func TestMeetingControllerStopDrainsFinalBeforeSealing(t *testing.T) {
	for _, immediate := range []bool{false, true} {
		t.Run(fmt.Sprintf("before-first-poll-%t", immediate), func(t *testing.T) {
			f := newControllerFixture(t)
			id := f.start(t)
			f.audio.stream.autoComplete.Store(false)
			if !immediate {
				f.audio.stream.emit(controllerState(id, 1, "starting"), controllerState(id, 1, "running"))
				waitController(t, f.c, func(v MeetingLiveView) bool { return v.State == "running" })
			}
			if err := f.c.ControlMeeting(context.Background(), "stop"); err != nil {
				t.Fatal(err)
			}
			if got := controllerControl(t, f.audio.stream); got != (controllerAudioControl{"stop", 1}) {
				t.Fatalf("stop lost initial native epoch: %+v", got)
			}
			if immediate {
				f.audio.stream.emit(controllerState(id, 1, "starting"))
			}
			f.audio.stream.emit(controllerState(id, 1, "stopping"))
			stopping := waitController(t, f.c, func(v MeetingLiveView) bool { return v.State == "stopping" })
			if stopping.AudioRecording == immediate {
				t.Fatal("stop changed capture claim before native completion acknowledgement")
			}
			f.audio.stream.emit(controllerWords(id, 1, "last-words", "Preserve the last words.", true), controllerState(id, 2, "completed"))
			v := waitController(t, f.c, func(v MeetingLiveView) bool { return v.State == "completed" })
			if len(v.Tail) != 1 || v.Meeting.State != meetings.Stopped || v.Meeting.EndedAt.IsZero() || v.AudioRecording {
				t.Fatalf("stop discarded final drain or failed to seal: %+v", v)
			}
			metadata, found, err := f.store.GetMeeting(context.Background(), id)
			if err != nil || !found || metadata.State != meetings.Stopped || metadata.RecordCount != 1 {
				t.Fatalf("durable final state differs from receipt: %+v found=%t err=%v", metadata, found, err)
			}
		})
	}
}

type controllerFaultRepo struct {
	meetings.Repository
	failNote    atomic.Bool
	entered     chan struct{}
	release     chan struct{}
	once        sync.Once
	releaseOnce sync.Once
}

var errControllerFixtureWrite = errors.New("fixture encrypted write failed")

func (r *controllerFaultRepo) Append(ctx context.Context, row meetings.Record) (meetings.Receipt, error) {
	if row.Kind == meetings.UserNote && r.failNote.Swap(false) {
		return meetings.Receipt{}, errControllerFixtureWrite
	}
	if row.Kind == meetings.TranscriptFinal && r.entered != nil {
		r.once.Do(func() { close(r.entered) })
		<-r.release
	}
	return r.Repository.Append(ctx, row)
}

func TestMeetingControllerTypedAgendaReusesMeetingAndNoteFailureCanRetry(t *testing.T) {
	f := newControllerFixture(t)
	repo := &controllerFaultRepo{Repository: f.store}
	f.c.meetings.repo = repo
	id, err := f.c.NewMeetingNotes(context.Background(), "Typed agenda", "en-US", []string{"microphone"})
	if err != nil {
		t.Fatal(err)
	}
	first := meetings.ManualNoteInput{ID: "agenda", Text: "Discuss launch risks\n"}
	if err := f.c.AddMeetingNote(context.Background(), id, first); err != nil {
		t.Fatal(err)
	}
	recordingAt := f.now.Add(2 * time.Minute)
	f.c.meetings.now = func() time.Time { return recordingAt }
	if got := f.start(t); got != id {
		t.Fatalf("recording opened a second meeting: %q != %q", got, id)
	}
	f.audio.stream.emit(controllerState(id, 1, "starting"), controllerState(id, 1, "running"))
	waitController(t, f.c, func(v MeetingLiveView) bool { return v.State == "running" })
	note := meetings.ManualNoteInput{ID: "retry-note", At: time.Second, Text: "  Keep exact note text.\n"}
	repo.failNote.Store(true)
	if err := f.c.AddMeetingNote(context.Background(), id, note); !errors.Is(err, errControllerFixtureWrite) {
		t.Fatal("note failure did not reach caller", err)
	}
	if v := f.c.MeetingLive(); v.State != "running" || v.Issue != "" {
		t.Fatalf("failed note poisoned ongoing speech: %+v", v)
	}
	if err := f.c.AddMeetingNote(context.Background(), id, note); err != nil {
		t.Fatalf("same exact note retry failed: %v", err)
	}
	f.audio.stream.emit(controllerWords(id, 1, "after-note-error", "Speech still works", true))
	waitController(t, f.c, func(v MeetingLiveView) bool { return len(v.Tail) >= 2 && v.Tail[len(v.Tail)-1].Record.Event.Text != "" })
	// A stop acknowledgement must follow the driver's final words. Disable the
	// convenience fake acknowledgement so it cannot overtake this queued final.
	f.audio.stream.autoComplete.Store(false)
	if err := f.c.ControlMeeting(context.Background(), "stop"); err != nil {
		t.Fatal(err)
	}
	if got := controllerControl(t, f.audio.stream); got != (controllerAudioControl{"stop", 1}) {
		t.Fatal(got)
	}
	last := controllerWords(id, 1, "stop-final", "Final words before stop acknowledgement", true)
	start, end := int64(1000), int64(2000)
	last.StartMS, last.EndMS = &start, &end
	f.audio.stream.emit(controllerState(id, 1, "stopping"), last, controllerState(id, 2, "completed"))
	waitController(t, f.c, func(v MeetingLiveView) bool { return v.State == "completed" })
	rows := controllerRecords(t, f, id)
	if len(rows) != 4 || rows[0].Record.Note.Text != first.Text || rows[1].Record.Note != note || rows[2].Record.Event.Text != "Speech still works" || rows[2].Record.Event.Start != 0 || rows[3].Record.Event.Text != last.Text || rows[3].Record.Event.Start != time.Second {
		t.Fatalf("retry lost/duplicated agenda or stopped final evidence: %+v", rows)
	}
	metadata, found, err := f.store.GetMeeting(context.Background(), id)
	if err != nil || !found || !metadata.RecordingStartedAt.Equal(recordingAt) || !metadata.StartedAt.Equal(f.now) {
		t.Fatalf("recording replaced creation time or lost its own anchor: %+v found=%t err=%v", metadata, found, err)
	}
	for _, check := range []struct {
		row   int
		start time.Time
		end   time.Time
	}{{0, f.now, time.Time{}}, {1, f.now.Add(time.Second), time.Time{}}, {2, recordingAt, recordingAt.Add(time.Second)}, {3, recordingAt.Add(time.Second), recordingAt.Add(2 * time.Second)}} {
		actualStart, actualEnd, err := meetings.RecordTimes(metadata, rows[check.row].Record)
		if err != nil || !actualStart.Equal(check.start) || !actualEnd.Equal(check.end) {
			t.Fatalf("row %d lost immutable absolute provenance: start=%s end=%s err=%v", check.row, actualStart, actualEnd, err)
		}
	}
}

func TestMeetingControllerLiveTailIsBoundedWithoutDroppingSavedHistory(t *testing.T) {
	for _, bytes := range []int{8, 12 << 10} {
		t.Run(fmt.Sprintf("row-bytes-%d", bytes), func(t *testing.T) {
			f := newControllerFixture(t)
			id := f.start(t)
			f.audio.stream.emit(controllerState(id, 1, "starting"), controllerState(id, 1, "running"))
			for i := 0; i < 27; i++ {
				event := controllerWords(id, 1, fmt.Sprintf("segment-%02d", i), fmt.Sprintf("%02d ", i)+strings.Repeat("a", bytes), true)
				start, end := int64(i*1000), int64((i+1)*1000)
				event.StartMS, event.EndMS = &start, &end
				f.audio.stream.emit(event)
			}
			v := waitController(t, f.c, func(v MeetingLiveView) bool {
				return len(v.Tail) > 0 && strings.HasPrefix(v.Tail[len(v.Tail)-1].Record.Event.Text, "26 ")
			})
			if len(v.Tail) > 20 || meetingTailBytes(v.Tail) > 128<<10 {
				t.Fatalf("live tail exceeded display bound: rows=%d bytes=%d", len(v.Tail), meetingTailBytes(v.Tail))
			}
			if rows := controllerRecords(t, f, id); len(rows) != 27 {
				t.Fatalf("bounding live tail erased durable history: %d", len(rows))
			}
		})
	}
}

func TestMeetingControllerInvalidationIsWriteBarrierAndLateCallbacksCannotResurrect(t *testing.T) {
	f := newControllerFixture(t)
	repo := &controllerFaultRepo{Repository: f.store, entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { repo.releaseOnce.Do(func() { close(repo.release) }) })
	f.c.meetings.repo = repo
	id := f.start(t)
	f.audio.stream.emit(controllerState(id, 1, "starting"), controllerState(id, 1, "running"), controllerWords(id, 1, "inflight", "Admitted before deletion", true))
	select {
	case <-repo.entered:
	case <-time.After(4 * time.Second):
		t.Fatal("write did not reach encrypted repository")
	}
	invalidated := make(chan struct{})
	go func() { f.c.NotifyMemoryChanging(); close(invalidated) }()
	select {
	case <-f.audio.stream.closed:
	case <-time.After(4 * time.Second):
		t.Fatal("invalidation did not immediately cancel native stream")
	}
	select {
	case <-invalidated:
		t.Fatal("invalidation returned while admitted write was still running")
	default:
	}
	repo.releaseOnce.Do(func() { close(repo.release) })
	select {
	case <-invalidated:
	case <-time.After(4 * time.Second):
		t.Fatal("write barrier failed to drain")
	}
	metadata, found, err := f.store.GetMeeting(context.Background(), id)
	if err != nil || !found || metadata.State != meetings.Failed || metadata.EndedAt.IsZero() {
		t.Fatalf("interrupted meeting still claims recording: %+v found=%t err=%v", metadata, found, err)
	}
	if err := f.store.DeleteMeeting(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	f.c.NotifyMemoryStable()
	f.audio.stream.emit(controllerWords(id, 1, "late", "Must not recreate deleted evidence", true), controllerState(id, 2, "completed"))
	f.c.meetings.wg.Wait()
	if v := f.c.MeetingLive(); v.Meeting.ID != "" || len(v.Tail) != 0 || len(v.Preview) != 0 || v.Issue != "memory_changed" {
		t.Fatalf("late callback republished deleted meeting: %+v", v)
	}
	if _, found, err := f.store.GetMeeting(context.Background(), id); err != nil || found {
		t.Fatalf("late callback recreated deleted metadata: found=%t err=%v", found, err)
	}
}

func TestMeetingControllerReadyStartInvalidationRaceClosesUnpublishedStream(t *testing.T) {
	f := newControllerFixture(t)
	id, err := f.c.NewMeetingNotes(context.Background(), "Agenda", "en-US", []string{"microphone"})
	if err != nil {
		t.Fatal(err)
	}
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	f.audio.onStart = func(ctx context.Context) { close(entered); <-ctx.Done(); close(cancelled); <-release }
	started := make(chan error, 1)
	go func() {
		started <- f.c.StartMeeting(context.Background(), "Ignored title", "nl-BE", []string{"system"})
	}()
	select {
	case <-entered:
	case <-time.After(4 * time.Second):
		t.Fatal("start did not reach native setup")
	}
	invalidated := make(chan struct{})
	go func() { f.c.NotifyMemoryChanging(); close(invalidated) }()
	select {
	case <-cancelled:
	case <-time.After(4 * time.Second):
		t.Fatal("memory invalidation did not cancel in-flight setup")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-started:
		if !errors.Is(err, ErrMemoryChanging) {
			t.Fatalf("invalidated setup reported success: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("canceled setup did not return")
	}
	select {
	case <-invalidated:
	case <-time.After(4 * time.Second):
		t.Fatal("setup invalidation barrier did not return")
	}
	f.c.NotifyMemoryStable()
	select {
	case <-f.audio.stream.closed:
	default:
		t.Fatal("unpublished native stream remained open")
	}
	if v := f.c.MeetingLive(); v.Meeting.ID != "" || v.State != "" {
		t.Fatalf("setup resurrected old ready meeting: %+v", v)
	}
	f.audio.mu.Lock()
	config := f.audio.config
	f.audio.mu.Unlock()
	if config.SessionID != id || config.Locale != "en-US" || len(config.Tracks) != 1 || config.Tracks[0] != "microphone" {
		t.Fatalf("typed agenda's frozen language/track was replaced: %+v", config)
	}
}

func TestMeetingControllerClientCloseDrainsBeforeStoreCanClose(t *testing.T) {
	f := newControllerFixture(t)
	id := f.start(t)
	f.audio.stream.autoComplete.Store(false)
	f.audio.stream.emit(controllerState(id, 1, "starting"), controllerState(id, 1, "running"))
	waitController(t, f.c, func(v MeetingLiveView) bool { return v.State == "running" })
	closed := make(chan struct{})
	go func() { f.c.Close(); close(closed) }()
	if got := controllerControl(t, f.audio.stream); got != (controllerAudioControl{"stop", 1}) {
		t.Fatal(got)
	}
	f.audio.stream.emit(controllerState(id, 1, "stopping"), controllerWords(id, 1, "close-final", "Final drain during close", true), controllerState(id, 2, "completed"))
	select {
	case <-closed:
	case <-time.After(4 * time.Second):
		t.Fatal("client close did not finish after native drain")
	}
	if rows := controllerRecords(t, f, id); len(rows) != 1 || rows[0].Record.Event.Text != "Final drain during close" {
		t.Fatalf("close discarded final evidence before store shutdown: %+v", rows)
	}
	if !f.audio.closed.Load() {
		t.Fatal("client close did not close the audio bridge")
	}
	if err := f.c.StartMeeting(context.Background(), "After close", "en-US", []string{"microphone"}); !errors.Is(err, ErrClientClosed) {
		t.Fatalf("closed client admitted new recording: %v", err)
	}
}
