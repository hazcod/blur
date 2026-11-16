package client

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/platform/darwin"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestStartMeetingNotesReopensSavedAgendaOnlyOnExplicitStart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "memory.db")
	key := securestore.OfflineFixtureKey()
	store, err := durable.OpenDurable(path, key, durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(store)
	t.Cleanup(func() { c.Close(); _ = store.Close() })
	created := time.Date(2026, 11, 12, 11, 0, 0, 0, time.UTC)
	c.meetings.now = func() time.Time { return created }
	c.meetings.open = func() (meetingAudio, error) {
		t.Error("creating an agenda opened audio")
		return nil, darwin.ErrMeetingAudioUnavailable
	}
	id, err := c.NewMeetingNotes(ctx, "Saved agenda title", "fr-BE", []string{"microphone", "system"})
	if err != nil {
		t.Fatal(err)
	}
	note := meetings.ManualNoteInput{ID: "yesterday", RecordedAt: created.Add(-time.Millisecond), Text: "  Keep yesterday's exact manual agenda.\n"}
	if err := c.AddMeetingNote(ctx, id, note); err != nil {
		t.Fatal(err)
	}
	before, err := c.MeetingTranscript(ctx, id, 0)
	if err != nil || len(before.Records) != 1 {
		t.Fatalf("saved agenda unavailable: rows=%d err=%v", len(before.Records), err)
	}
	c.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = durable.OpenDurable(path, key, durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	c = NewClient(store)
	s := &controllerAudioStream{batches: make(chan []darwin.MeetingAudioEvent, 64), controls: make(chan controllerAudioControl, 32), closed: make(chan struct{})}
	s.autoComplete.Store(true)
	audio := &controllerAudioFixture{stream: s}
	var opens atomic.Int64
	c.meetings.open = func() (meetingAudio, error) { opens.Add(1); return audio, nil }
	recording := created.Add(25 * time.Hour)
	c.meetings.now = func() time.Time { return recording }
	if _, err := c.MeetingPage(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.MeetingTranscript(ctx, id, 0); err != nil {
		t.Fatal(err)
	}
	if opens.Load() != 0 || c.MeetingLive().Meeting.ID != "" {
		t.Fatal("opening saved agenda implicitly activated recording")
	}
	if err := c.StartMeetingNotes(ctx, id); err != nil {
		t.Fatal(err)
	}
	audio.mu.Lock()
	config := audio.config
	audio.mu.Unlock()
	if config.SessionID != id || config.Locale != "fr-BE" || !reflect.DeepEqual(config.Tracks, []string{"microphone", "system"}) {
		t.Fatal("explicit start replaced saved identity, language or tracks")
	}
	v := c.MeetingLive()
	if v.State != "starting" || v.AudioRecording || v.Meeting.ID != id || v.Meeting.Title != "Saved agenda title" || !reflect.DeepEqual(v.Tail, before.Records) {
		t.Fatal("explicit start lost saved agenda or claimed running before acknowledgement")
	}
	saved, found, err := store.GetMeeting(ctx, id)
	if err != nil || !found || saved.State != meetings.Ready || !saved.RecordingStartedAt.IsZero() {
		t.Fatal("recording origin persisted before native admission")
	}
	s.emit(controllerState(id, 1, "starting"), controllerWords(id, 1, "first", "todayrecordingmarker first words", true), controllerState(id, 1, "running"))
	v = waitController(t, c, func(v MeetingLiveView) bool { return v.State == "running" && len(v.Tail) == 2 })
	if !v.Meeting.RecordingStartedAt.Equal(recording) || v.Tail[0] != before.Records[0] || v.Tail[1].Record.Event.Start != 0 || v.Tail[1].Record.Event.End != time.Second {
		t.Fatal("first final inherited agenda age or rewrote manual provenance")
	}
	page, err := c.MeetingTranscript(ctx, id, 0)
	if err != nil || len(page.Records) != 2 || page.Records[0] != before.Records[0] {
		t.Fatal("reopened agenda source identity or text changed")
	}
	hits, err := store.SearchExact(ctx, search.ExactQuery{Terms: []string{"todayrecordingmarker"}, CapturedStart: recording, CapturedEnd: recording.Add(2 * time.Second), TopK: 5, MaxRows: 10, MaxBytes: search.MaxIndexBytes})
	if err != nil || len(hits.Hits) != 1 || hits.Hits[0].SourceID != page.Records[1].SourceID || !hits.Hits[0].CapturedAt.Equal(recording.Add(time.Second)) {
		t.Fatal("first source did not use the newly acknowledged recording clock", err)
	}
}

func TestStartMeetingNotesRestoresBoundedTailAndCursor(t *testing.T) {
	f := newControllerFixture(t)
	ctx := context.Background()
	id, err := f.c.NewMeetingNotes(ctx, "Many agenda notes", "en-US", []string{"microphone"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 27; i++ {
		note := meetings.ManualNoteInput{ID: fmt.Sprintf("note-%02d", i), Text: fmt.Sprintf("%02d ", i) + strings.Repeat("a", 12<<10)}
		if err := f.c.AddMeetingNote(ctx, id, note); err != nil {
			t.Fatal(err)
		}
	}
	all := controllerRecords(t, f, id)
	if err := f.store.DeleteObservationRecord(ctx, all[20].SourceID); err != nil {
		t.Fatal(err)
	}
	// Simulate a cleared in-memory selection while retaining the encrypted agenda.
	f.c.meetings.invalidate(false)
	if err := f.c.StartMeetingNotes(ctx, id); err != nil {
		t.Fatal(err)
	}
	v := f.c.MeetingLive()
	if len(v.Tail) == 0 || len(v.Tail) > 20 || meetingTailBytes(v.Tail) > 128<<10 || v.Tail[len(v.Tail)-1] != all[len(all)-1] {
		t.Fatal("restored agenda tail is missing, unbounded or not the saved tail")
	}
	for _, row := range v.Tail {
		if row.SourceID == all[20].SourceID {
			t.Fatal("erased note restored into live tail")
		}
	}
	f.audio.stream.emit(controllerState(id, 1, "starting"), controllerWords(id, 1, "new-final", "Fresh final after restored notes", true))
	v = waitController(t, f.c, func(v MeetingLiveView) bool {
		return len(v.Tail) > 0 && v.Tail[len(v.Tail)-1].Record.Kind == meetings.TranscriptFinal
	})
	seen := map[string]bool{}
	for _, row := range v.Tail {
		if seen[row.SourceID] {
			t.Fatal("restored cursor replayed old notes into live tail")
		}
		seen[row.SourceID] = true
	}
	if rows := controllerRecords(t, f, id); len(rows) != 27 {
		t.Fatal("starting bounded tail lost or duplicated saved records")
	}
}

type persistedAgendaAudio struct {
	*controllerAudioFixture
	availability darwin.MeetingAudioAvailability
	accessErr    error
	startErr     error
	probed       string
	onProbe      func(context.Context) error
}

func (a *persistedAgendaAudio) Probe(ctx context.Context, locale string) (darwin.MeetingAudioAvailability, error) {
	a.probed = locale
	if a.onProbe != nil {
		if err := a.onProbe(ctx); err != nil {
			return darwin.MeetingAudioAvailability{}, err
		}
	}
	return a.availability, nil
}
func (a *persistedAgendaAudio) RequestAccess(context.Context, []string) error { return a.accessErr }
func (a *persistedAgendaAudio) StartMeeting(ctx context.Context, config darwin.MeetingAudioStart) (meetingAudioStream, error) {
	if a.startErr != nil {
		return nil, a.startErr
	}
	return a.controllerAudioFixture.StartMeeting(ctx, config)
}

func TestStartMeetingNotesFailuresPreserveReadyAndRetrySameAgenda(t *testing.T) {
	for _, failure := range []string{"library", "assets", "permission", "admission"} {
		t.Run(failure, func(t *testing.T) {
			f := newControllerFixture(t)
			ctx := context.Background()
			id, err := f.c.NewMeetingNotes(ctx, "Retriable saved agenda", "nl-BE", []string{"microphone"})
			if err != nil {
				t.Fatal(err)
			}
			if err := f.c.AddMeetingNote(ctx, id, meetings.ManualNoteInput{ID: "agenda", Text: "Do not lose this saved agenda"}); err != nil {
				t.Fatal(err)
			}
			before := controllerRecords(t, f, id)
			f.c.meetings.invalidate(false)
			a := &persistedAgendaAudio{controllerAudioFixture: f.audio, availability: darwin.MeetingAudioAvailability{SpeechAvailable: true, RequestedLocaleInstalled: true, MicrophonePermission: "authorized"}}
			fixtureErr := errors.New("fixture: native setup refused")
			f.c.meetings.open = func() (meetingAudio, error) {
				if failure == "library" {
					return nil, fixtureErr
				}
				return a, nil
			}
			switch failure {
			case "assets":
				a.availability.RequestedLocaleInstalled = false
			case "permission":
				a.availability.MicrophonePermission = "denied"
				a.accessErr = fixtureErr
			case "admission":
				a.startErr = fixtureErr
			}
			if err := f.c.StartMeetingNotes(ctx, id); err == nil {
				t.Fatal("native refusal claimed successful start")
			}
			saved, found, err := f.store.GetMeeting(ctx, id)
			if err != nil || !found || saved.State != meetings.Ready || saved.Revision != 1 || !saved.RecordingStartedAt.IsZero() || !reflect.DeepEqual(controllerRecords(t, f, id), before) {
				t.Fatal("failed start changed or erased persisted Ready agenda")
			}
			if v := f.c.MeetingLive(); v.State != "ready" || v.Meeting.ID != id || v.AudioRecording || !v.Meeting.RecordingStartedAt.IsZero() || !reflect.DeepEqual(v.Tail, before) {
				t.Fatal("failed explicit start lost the retriable Ready view")
			}
			a.availability.RequestedLocaleInstalled, a.availability.MicrophonePermission = true, "authorized"
			a.accessErr, a.startErr = nil, nil
			f.c.meetings.open = func() (meetingAudio, error) { return a, nil }
			if err := f.c.StartMeetingNotes(ctx, id); err != nil || f.c.MeetingLive().Meeting.ID != id || a.probed != "nl-BE" {
				t.Fatal("failed start was not retriable with the same saved settings", err)
			}
		})
	}
}

func TestStartMeetingNotesRejectsMissingNonReadyAndCanceledBeforeAudio(t *testing.T) {
	f := newControllerFixture(t)
	ctx := context.Background()
	var opens atomic.Int64
	f.c.meetings.open = func() (meetingAudio, error) { opens.Add(1); return f.audio, nil }
	if err := f.c.StartMeetingNotes(ctx, "missing-agenda"); !errors.Is(err, meetings.ErrNotFound) {
		t.Fatal("missing selected agenda was replaced by a new meeting", err)
	}
	for _, state := range []meetings.State{meetings.Running, meetings.Paused, meetings.Stopped, meetings.Failed} {
		m := meetings.Meeting{ID: "state-" + string(state), Title: "Saved meeting", Locale: "en-US", StartedAt: f.now, State: state, Tracks: []string{"microphone"}, Revision: 1}
		if state == meetings.Stopped {
			m.EndedAt = f.now.Add(time.Minute)
		}
		if err := f.store.PutMeeting(ctx, m, 0); err != nil {
			t.Fatal(err)
		}
		if err := f.c.StartMeetingNotes(ctx, m.ID); !errors.Is(err, meetings.ErrState) {
			t.Fatalf("selected %s meeting was restarted: %v", state, err)
		}
	}
	id, err := f.c.NewMeetingNotes(ctx, "Ready", "en-US", []string{"microphone"})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := f.c.StartMeetingNotes(canceled, id); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled start changed a Ready agenda", err)
	}
	if opens.Load() != 0 {
		t.Fatal("invalid, missing, non-Ready or canceled selection opened audio")
	}
}

func TestStartMeetingNotesSelectsSavedAgendaInsteadOfDifferentLiveReady(t *testing.T) {
	f := newControllerFixture(t)
	ctx := context.Background()
	selected, err := f.c.NewMeetingNotes(ctx, "Selected agenda", "fr-BE", []string{"system"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.c.AddMeetingNote(ctx, selected, meetings.ManualNoteInput{ID: "selected", Text: "Selected agenda evidence"}); err != nil {
		t.Fatal(err)
	}
	other, err := f.c.NewMeetingNotes(ctx, "Other in-memory agenda", "en-US", []string{"microphone"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.c.StartMeetingNotes(ctx, selected); err != nil {
		t.Fatal(err)
	}
	if v := f.c.MeetingLive(); v.Meeting.ID != selected || v.Meeting.Title != "Selected agenda" || len(v.Tail) != 1 || v.Tail[0].Record.Note.ID != "selected" {
		t.Fatal("explicit selected agenda started the other live Ready agenda")
	}
	saved, found, err := f.store.GetMeeting(ctx, other)
	if err != nil || !found || saved.State != meetings.Ready || !saved.RecordingStartedAt.IsZero() {
		t.Fatal("starting selected agenda altered another Ready agenda")
	}
}

type persistedAgendaScanRepo struct {
	meetings.Repository
	pages int
}

func (r *persistedAgendaScanRepo) MeetingRecords(_ context.Context, _ string, p meetings.PageRequest) (meetings.RecordPage, error) {
	r.pages++
	return meetings.RecordPage{Next: p.After + 1, More: true}, nil
}

func TestStartMeetingNotesBoundsErasedHistoryScanBeforeAudio(t *testing.T) {
	f := newControllerFixture(t)
	ctx := context.Background()
	id, err := f.c.NewMeetingNotes(ctx, "Empty agenda", "en-US", []string{"microphone"})
	if err != nil {
		t.Fatal(err)
	}
	repo := &persistedAgendaScanRepo{Repository: f.store}
	f.c.meetings.repo = repo
	var opens atomic.Int64
	f.c.meetings.open = func() (meetingAudio, error) { opens.Add(1); return f.audio, nil }
	if err := f.c.StartMeetingNotes(ctx, id); !errors.Is(err, meetings.ErrLimit) {
		t.Fatal("unbounded erased history scan did not fail visibly", err)
	}
	if repo.pages != meetings.DefaultLimits().MaxNotes+1 || opens.Load() != 0 {
		t.Fatal("agenda restoration exceeded bounded paging or opened audio on failure")
	}
}

func TestStartMeetingNotesRejectsLegacyFinalsBeforeRebasing(t *testing.T) {
	f := newControllerFixture(t)
	ctx := context.Background()
	m := meetings.Meeting{ID: "legacy-ready-with-final", Title: "Legacy recording", Locale: "en-US", StartedAt: f.now, State: meetings.Running, Tracks: []string{"microphone"}, Revision: 1}
	if err := f.store.PutMeeting(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
	r := meetings.Record{Key: m.ID + "/transcript/microphone/old", Kind: meetings.TranscriptFinal, SessionID: m.ID, Event: meetings.Event{Kind: meetings.Final, SessionID: m.ID, TrackID: "microphone", SegmentID: "old", Revision: 1, End: time.Second, Text: "Legacy evidence clock remains unchanged"}}
	if _, err := f.store.Append(ctx, r); err != nil {
		t.Fatal(err)
	}
	m.State, m.Revision = meetings.Ready, 2
	if err := f.store.PutMeeting(ctx, m, 1); err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int64
	f.c.meetings.open = func() (meetingAudio, error) { opens.Add(1); return f.audio, nil }
	if err := f.c.StartMeetingNotes(ctx, m.ID); !errors.Is(err, meetings.ErrState) {
		t.Fatal("Ready metadata with existing finals was rebased and recorded again", err)
	}
	if opens.Load() != 0 {
		t.Fatal("unsafe legacy rebase opened audio before rejecting transcript evidence")
	}
}

func TestStartMeetingNotesInvalidationCancelsSetupAndKeepsReadyPersisted(t *testing.T) {
	f := newControllerFixture(t)
	ctx := context.Background()
	id, err := f.c.NewMeetingNotes(ctx, "Cancelable agenda", "en-US", []string{"microphone"})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	audio := &persistedAgendaAudio{controllerAudioFixture: f.audio, onProbe: func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}}
	f.c.meetings.open = func() (meetingAudio, error) { return audio, nil }
	started := make(chan error, 1)
	go func() { started <- f.c.StartMeetingNotes(ctx, id) }()
	select {
	case <-entered:
	case <-time.After(4 * time.Second):
		t.Fatal("selected agenda setup did not reach synthetic audio boundary")
	}
	f.c.NotifyMemoryChanging()
	defer f.c.NotifyMemoryStable()
	select {
	case err := <-started:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("memory invalidation did not cancel selected agenda setup", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("memory invalidation deadlocked with selected agenda setup")
	}
	saved, found, err := f.store.GetMeeting(ctx, id)
	if err != nil || !found || saved.State != meetings.Ready || !saved.RecordingStartedAt.IsZero() || f.c.MeetingLive().State != "" {
		t.Fatal("canceled setup changed persisted agenda or resurrected live selection")
	}
}
