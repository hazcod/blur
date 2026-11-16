package client

import (
	"context"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/meetings"
)

func TestMeetingMenuOpensOverlayWithoutRaisingDashboard(t *testing.T) {
	main := &dashboardPresentWindow{}
	app := &dashboardPresentApp{main: main}
	d := &desktop{app: app, page: "Settings", dashboardOpen: true}
	d.openMeetingsFromMenu()
	if !d.meetingUI.overlayOpen || !d.meetingUI.overlayMounted || d.page != "Settings" || main.shows != 0 {
		t.Fatalf("meeting shortcut raised dashboard: page=%s shows=%d overlay=%t", d.page, main.shows, d.meetingUI.overlayOpen)
	}
	if d.meetingUI.busy || d.meetingUI.live.State != "" {
		t.Fatal("opening voice note overlay started audio")
	}
}

func TestExplicitSettingsOverridesMeetingOverlay(t *testing.T) {
	main := &dashboardPresentWindow{}
	app := &dashboardPresentApp{main: main}
	d := &desktop{app: app, page: "Voice notes", dashboardOpen: true, powerStatus: "checked", meetingUI: meetingViewState{overlayOpen: true}}
	d.openSettingsFromMenu()
	if d.meetingUI.overlayOpen || d.page != "Settings" || main.shows != 1 {
		t.Fatal("voice note overlay took priority over explicit Settings")
	}
}

func TestOpeningOverlayClearsArchivedNoteTarget(t *testing.T) {
	app := &dashboardPresentApp{}
	d := &desktop{app: app, page: "Voice notes", meetingUI: meetingViewState{selected: "archived", selectedTitle: "Yesterday"}}
	d.openMeetingsFromMenu()
	if d.meetingUI.selected != "" || d.meetingUI.selectedTitle != "" {
		t.Fatal("overlay silently targeted archived meeting")
	}
}

func TestOpeningOverlayPreservesSelectedSavedReadyNote(t *testing.T) {
	app := &dashboardPresentApp{}
	d := &desktop{app: app, page: "Voice notes", meetingUI: meetingViewState{selected: "saved-ready", selectedTitle: "Personal thoughts", selectedState: meetings.Ready}}
	d.openMeetingsFromMenu()
	if d.meetingUI.selected != "saved-ready" || d.meetingUI.selectedState != meetings.Ready {
		t.Fatal("overlay discarded the selected saved note before explicit recording")
	}
}

func TestOpeningOverlaySyncsCurrentReadySettings(t *testing.T) {
	f := newControllerFixture(t)
	id, err := f.c.NewMeetingNotes(context.Background(), "Current personal note", "en-US", []string{"microphone"})
	if err != nil {
		t.Fatal(err)
	}
	app := &dashboardPresentApp{}
	d := &desktop{app: app, client: f.c, page: "Voice notes", meetingUI: meetingViewState{
		// Keep the fake UI's probe in flight: this test checks selection, not
		// asynchronous layout or native availability callbacks.
		selected: "different-ready", selectedState: meetings.Ready, title: "Other note", locale: "nl-BE", system: true, loaded: true, probing: true, probedAt: time.Now(),
	}}
	d.openMeetingsFromMenu()
	if d.meetingUI.selected != id || d.meetingUI.title != "Current personal note" || d.meetingUI.locale != "en-US" || !d.meetingUI.microphone || d.meetingUI.system {
		t.Fatal("overlay displayed different settings from the current saved note")
	}
}

func TestInvalidationCancelsVoiceNoteSetup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &desktop{meetingUI: meetingViewState{operationCancel: cancel}}
	d.invalidateMeetingViews()
	if ctx.Err() == nil || d.meetingUI.operationCancel != nil {
		t.Fatal("memory invalidation left a compound setup operation alive")
	}
}

func TestTypedOnlyNoteNeedsNoEnabledAudioTrack(t *testing.T) {
	f := newControllerFixture(t)
	id, err := f.c.NewMeetingNotes(context.Background(), "Personal thoughts", "en-US", nil)
	if err != nil {
		t.Fatalf("typed note without enabled audio: %v", err)
	}
	if err := f.c.AddMeetingNote(context.Background(), id, meetings.ManualNoteInput{ID: "personal-note", RecordedAt: f.now, Text: "Remember this thought."}); err != nil {
		t.Fatal(err)
	}
	if f.c.MeetingLive().State != "ready" || f.c.MeetingLive().AudioRecording || f.audio.config.SessionID != "" {
		t.Fatal("typed note started audio")
	}
}

type meetingOperationTestApp struct {
	quickDashboardApp
	posted chan func()
}

func (a *meetingOperationTestApp) Post(fn func()) { a.posted <- fn }

func (a *meetingOperationTestApp) finish(t *testing.T) {
	t.Helper()
	select {
	case fn := <-a.posted:
		fn()
	case <-time.After(5 * time.Second):
		t.Fatal("voice note operation did not finish")
	}
}

func TestVoiceNoteOperationKeepsMemoryCancellationBetweenClientCalls(t *testing.T) {
	f := newControllerFixture(t)
	app := &meetingOperationTestApp{posted: make(chan func(), 8)}
	d := &desktop{client: f.c, app: app}
	started, release := make(chan context.Context, 1), make(chan struct{})
	d.meetingOperation(func(ctx context.Context) error {
		started <- ctx
		<-release
		return ctx.Err()
	}, func() { t.Error("invalidated setup published a success receipt") })
	var ctx context.Context
	select {
	case ctx = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("setup did not start")
	}
	// No UI invalidation is delivered: the Client epoch must cancel setup even
	// when the UI queue has not processed the memory-change event yet.
	f.c.NotifyMemoryChanging()
	f.c.NotifyMemoryStable()
	if ctx.Err() == nil {
		t.Error("compound setup survived a memory mutation between Client calls")
	}
	close(release)
	app.finish(t)
}

func TestVoiceNoteAppendRetryKeepsNewSavedIdentity(t *testing.T) {
	f := newControllerFixture(t)
	repo := &controllerFaultRepo{Repository: f.store}
	repo.failNote.Store(true)
	f.c.meetings.repo = repo
	app := &meetingOperationTestApp{posted: make(chan func(), 8)}
	d := &desktop{client: f.c, app: app, meetingUI: meetingViewState{locale: "en-US", draft: "A personal thought"}}
	d.saveMeetingNote()
	app.finish(t)
	id, noteID := d.meetingUI.selected, d.meetingUI.noteID
	if id == "" || noteID == "" || d.meetingUI.draft == "" || d.meetingUI.selectedState != meetings.Ready || !d.meetingUI.microphone {
		t.Fatal("append failure lost the saved note, draft, retry key or displayed settings")
	}
	d.saveMeetingNote()
	app.finish(t)
	page, err := f.c.MeetingTranscript(context.Background(), id, 0)
	if err != nil || len(page.Records) != 1 || page.Records[0].Record.Note.ID != noteID || d.meetingUI.selected != id || d.meetingUI.draft != "" {
		t.Fatalf("retry did not append once to the same note: records=%d err=%v", len(page.Records), err)
	}
	if f.c.MeetingLive().AudioRecording || f.audio.config.SessionID != "" {
		t.Fatal("typing or retrying a note started audio")
	}
}
