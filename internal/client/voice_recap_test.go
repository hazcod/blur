package client

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/model"
)

func finishVoiceRecapWork(t *testing.T, app *meetingOperationTestApp, d *desktop) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for d.memoryUI.recap.loading || d.meetingUI.reading {
		select {
		case receipt := <-app.posted:
			receipt()
		case <-deadline.C:
			t.Fatal("finished voice recap stalled", d.memoryUI.recap.status)
		}
	}
}

func TestFinishedVoiceRecapReadsAllPagesWithGenuineSourcesInAsk(t *testing.T) {
	c, store, model, meeting := summaryFixture(t, 55)
	meeting.RecordCount = 55
	app := &meetingOperationTestApp{posted: make(chan func(), 128)}
	d := &desktop{app: app, client: c, page: "Settings", quickPanelPinned: true, tokens: themePalette(false)}
	d.meetingUI.live = MeetingLiveView{State: "completed", Meeting: meeting}
	d.openFinishedVoiceRecap(meeting.ID)
	finishVoiceRecapWork(t, app, d)
	u := &d.memoryUI.recap
	if d.page != "Ask" || !d.dashboardOpen || d.quickPanelPinned || !d.memoryUI.meetingDetail || d.meetingUI.selected != meeting.ID {
		t.Fatal("completion did not open the saved note in the main Ask screen")
	}
	if !u.complete || u.partial || u.loading || u.processed != 55 || len(u.parts) != 2 {
		t.Fatalf("recap claimed the wrong coverage: complete=%v partial=%v processed=%d parts=%d status=%s", u.complete, u.partial, u.processed, len(u.parts), u.status)
	}
	seen := map[string]bool{}
	for _, part := range u.parts {
		for _, source := range part.result.Sources {
			saved, found, err := store.GetObservation(context.Background(), source.SourceID)
			if err != nil || !found || saved.Text() != source.Text || seen[source.SourceID] {
				t.Fatal("recap lost or forged a genuine saved source", err)
			}
			seen[source.SourceID] = true
		}
	}
	model.mu.Lock()
	calls := model.calls
	model.mu.Unlock()
	if calls != 2 || len(seen) != 55 {
		t.Fatal("automatic recap skipped a page", calls, len(seen))
	}
	_, buttons, text := memoryWidgetTree(t, d)
	if !strings.Contains(text, "Voice note recap") || !strings.Contains(text, "All 55 saved passages") || len(buttons["Show transcript"]) != 1 || strings.Contains(text, "Saved decision 0:") {
		t.Fatal("mounted recap lost full-coverage copy or paged original transcript")
	}
	clickMemoryButton(t, buttons, "Show transcript")
	_, buttons, _ = memoryWidgetTree(t, d)
	if len(buttons["Later"]) != 1 {
		t.Fatal("expanded recap transcript lost original paging")
	}
}

func TestAutomaticVoiceRecapNeverUsesConsentedRemoteProvider(t *testing.T) {
	c, _, model, meeting := summaryFixture(t, 1)
	c.localAI.settings.Provider, c.localAI.settings.RemoteAskEnabled = ProviderClaude, true
	result, page, err := c.SummarizeLocalMeetingPage(context.Background(), meeting.ID, 0)
	if !errors.Is(err, assistant.ErrAskUnavailable) || result.Answer.Text != "" || len(page.Records) != 0 || model.calls != 0 {
		t.Fatal("automatic recap admitted a remote model", err)
	}
	app := &meetingOperationTestApp{posted: make(chan func(), 128)}
	d := &desktop{app: app, client: c, page: "Ask", tokens: themePalette(false)}
	d.meetingUI.live = MeetingLiveView{State: "completed", Meeting: meeting}
	d.openFinishedVoiceRecap(meeting.ID)
	finishVoiceRecapWork(t, app, d)
	_, buttons, _ := memoryWidgetTree(t, d)
	clickMemoryButton(t, buttons, "Show transcript")
	_, _, text := memoryWidgetTree(t, d)
	if !strings.Contains(text, "An on-device recap is unavailable") || !strings.Contains(text, "Saved decision 0") || model.calls != 0 {
		t.Fatal("unavailable automatic recap hid the saved words or used remote generation")
	}
}

func TestFinishedVoiceRecapIgnoresArchiveAndActiveRecording(t *testing.T) {
	c, _, model, meeting := summaryFixture(t, 1)
	app := &meetingOperationTestApp{posted: make(chan func(), 128)}
	d := &desktop{app: app, client: c, page: "Settings"}
	d.meetingUI.live = MeetingLiveView{State: "running", Meeting: meeting}
	d.openFinishedVoiceRecap(meeting.ID)
	d.meetingUI.live.State = "completed"
	d.openFinishedVoiceRecap("an-unrelated-archive")
	if d.page != "Settings" || d.dashboardOpen || d.memoryUI.recap.loading || model.calls != 0 || len(app.posted) != 0 {
		t.Fatal("archive or active capture caused automatic recap navigation")
	}
}

func TestExplicitArchivedVoiceRecapReadsAllPagesWithoutOpeningRecorder(t *testing.T) {
	c, _, model, meeting := summaryFixture(t, 55)
	meeting.RecordCount = 55
	app := &meetingOperationTestApp{posted: make(chan func(), 128)}
	d := &desktop{app: app, client: c, page: "Ask", dashboardOpen: true, tokens: themePalette(false)}
	d.openMemoryMeeting(meeting)
	app.finish(t)
	_, buttons, text := memoryWidgetTree(t, d)
	if model.calls != 0 || strings.Contains(text, "Saved decision 0:") {
		t.Fatal("opening archive inferred or exposed transcript before explicit action")
	}
	clickMemoryButton(t, buttons, "Recap voice note")
	finishVoiceRecapWork(t, app, d)
	u := &d.memoryUI.recap
	_, buttons, text = memoryWidgetTree(t, d)
	if !u.complete || u.partial || u.processed != 55 || len(u.parts) != 2 || d.meetingUI.overlayOpen || d.meetingUI.startOnShow || c.MeetingLive().AudioRecording || strings.Contains(text, "Saved decision 0:") || len(buttons["Show transcript"]) != 1 {
		t.Fatal("explicit archive recap lost full-source pipeline or changed recorder/disclosure", u.status, text)
	}
}

func TestExplicitArchivedVoiceRecapCannotReplaceEditorOrActiveRecording(t *testing.T) {
	c, _, model, meeting := summaryFixture(t, 1)
	app := &meetingOperationTestApp{posted: make(chan func(), 128)}
	d := &desktop{app: app, client: c, page: "Ask", tokens: themePalette(false)}
	d.selectMemoryMeeting(meeting)
	d.noteUI.open, d.noteUI.draft = true, "Unfinished Markdown draft"
	d.recapSelectedVoiceNote()
	if !d.noteUI.open || d.noteUI.draft != "Unfinished Markdown draft" || d.memoryUI.recap.id != "" || model.calls != 0 {
		t.Fatal("explicit recap replaced an open editor")
	}
	d.noteUI.open = false
	d.meetingUI.live = MeetingLiveView{State: "running", Meeting: meeting, AudioRecording: true}
	d.recapSelectedVoiceNote()
	if d.memoryUI.recap.id != "" || model.calls != 0 {
		t.Fatal("saved recap action inferred over the active recording")
	}
	d.meetingUI.live = MeetingLiveView{}
	d.page = "Today"
	d.recapSelectedVoiceNote()
	if d.page != "Today" || d.memoryUI.recap.id != "" || model.calls != 0 || len(app.posted) != 0 {
		t.Fatal("stale recap action stole focus from another page")
	}
}

func TestExplicitArchivedVoiceRecapRejectsLateResultAfterSelectionChange(t *testing.T) {
	c, store, model, meeting := summaryFixture(t, 1)
	other := meeting
	other.ID, other.Title, other.State = "other-saved-voice", "Another saved voice note", meetings.Stopped
	other.EndedAt = other.StartedAt.Add(time.Minute)
	if err := store.PutMeeting(t.Context(), other, 0); err != nil {
		t.Fatal(err)
	}
	model.started, model.release = make(chan context.Context, 1), make(chan struct{})
	app := &meetingOperationTestApp{posted: make(chan func(), 128)}
	d := &desktop{app: app, client: c, page: "Ask", tokens: themePalette(false)}
	d.openMemoryMeeting(meeting)
	app.finish(t)
	d.memoryUI.recap.resumeEditor, d.noteUI.draft = true, "Suspended Markdown draft"
	d.recapSelectedVoiceNote()
	var inference context.Context
	select {
	case inference = <-model.started:
	case <-time.After(5 * time.Second):
		close(model.release)
		t.Fatal("explicit saved recap did not reach selected model")
	}
	d.openMemoryMeeting(other)
	if inference.Err() == nil || !d.memoryUI.recap.resumeEditor {
		close(model.release)
		t.Fatal("switching saved note kept inference alive or lost suspended draft")
	}
	close(model.release)
	app.finish(t) // New saved transcript or canceled old recap receipt.
	app.finish(t)
	if d.meetingUI.selected != other.ID || len(d.meetingUI.transcript.Records) != 0 || d.memoryUI.recap.id != "" || len(d.memoryUI.recap.parts) != 0 || !d.memoryUI.recap.resumeEditor {
		t.Fatal("late explicit recap restored the previous saved note")
	}
	d.activateSidebarPage("Ask")
	if !d.noteUI.open || d.noteUI.draft != "Suspended Markdown draft" {
		t.Fatal("switching saved notes made an interrupted Markdown draft unreachable")
	}
}

func TestFinishedVoiceRecapInvalidationRejectsLateModelResult(t *testing.T) {
	c, _, model, meeting := summaryFixture(t, 1)
	model.started, model.release = make(chan context.Context, 1), make(chan struct{})
	app := &meetingOperationTestApp{posted: make(chan func(), 128)}
	d := &desktop{app: app, client: c, page: "Ask", tokens: themePalette(false)}
	installNoteDocumentTestInvalidator(c, app, d)
	d.meetingUI.live = MeetingLiveView{State: "completed", Meeting: meeting}
	d.openFinishedVoiceRecap(meeting.ID)
	var modelContext context.Context
	select {
	case modelContext = <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("recap never reached local model")
	}
	runNoteDocumentMutation(t, app, func() error { c.NotifyMemoryChanging(); c.NotifyMemoryStable(); return nil })
	if modelContext.Err() == nil {
		t.Fatal("erasure did not cancel recap model context")
	}
	close(model.release)
	// The final worker receipt must be rejected by both request and epoch.
	app.finish(t)
	if len(d.memoryUI.recap.parts) != 0 || d.memoryUI.recap.id != "" || d.memoryUI.recap.loading || d.memoryUI.meetingDetail || d.meetingUI.selected != "" {
		t.Fatal("late recap restored erased voice-note content")
	}
}

func TestFinishedVoiceRecapProcessingLimitNeverClaimsWholeNote(t *testing.T) {
	c, _, _, meeting := summaryFixture(t, 51)
	app := &meetingOperationTestApp{posted: make(chan func(), 128)}
	d := &desktop{app: app, client: c, page: "Ask", tokens: themePalette(false)}
	read, stable := c.beginVoicePublication()
	if !stable {
		t.Fatal("fixture memory unstable")
	}
	d.memoryUI.recap = voiceRecapViewState{id: meeting.ID, request: 1, loading: true, total: 51}
	d.selectMemoryMeeting(meeting)
	go d.runFinishedVoiceRecap(context.Background(), meeting.ID, read, 1, 0, 1)
	finishVoiceRecapWork(t, app, d)
	u := &d.memoryUI.recap
	_, _, text := memoryWidgetTree(t, d)
	if u.complete || !u.partial || u.processed != 50 || !strings.Contains(text, "reached its processing limit") || strings.Contains(text, "All 51 saved passages") {
		t.Fatal("bounded recap claimed complete coverage", u.status, u.processed)
	}
}

func TestFinishedVoiceRecapDoesNotRenderGeneralAskRefusal(t *testing.T) {
	c, _, model, meeting := summaryFixture(t, 3)
	model.insufficient = true
	app := &meetingOperationTestApp{posted: make(chan func(), 128)}
	d := &desktop{app: app, client: c, page: "Ask", tokens: themePalette(false)}
	d.meetingUI.live = MeetingLiveView{State: "completed", Meeting: meeting}
	d.openFinishedVoiceRecap(meeting.ID)
	finishVoiceRecapWork(t, app, d)
	_, buttons, _ := memoryWidgetTree(t, d)
	clickMemoryButton(t, buttons, "Show transcript")
	_, _, text := memoryWidgetTree(t, d)
	if len(d.memoryUI.recap.parts) != 0 || d.memoryUI.recap.complete || strings.Contains(text, assistant.InsufficientEvidenceText) || strings.Contains(text, "Sources: saved passages") || strings.Contains(text, "unavailable with the current model") || !strings.Contains(text, "understandable content") || !strings.Contains(text, "Saved decision 0") {
		t.Fatal("Ask refusal or misleading provider error replaced a voice recap", d.memoryUI.recap.status)
	}
}

func TestFinishedVoiceRecapContinuesAfterContentlessPrefix(t *testing.T) {
	c, _, model, meeting := summaryFixture(t, 51)
	model.insufficientCalls = 1
	meeting.RecordCount = 51
	app := &meetingOperationTestApp{posted: make(chan func(), 128)}
	d := &desktop{app: app, client: c, page: "Ask", tokens: themePalette(false)}
	d.meetingUI.live = MeetingLiveView{State: "completed", Meeting: meeting}
	d.openFinishedVoiceRecap(meeting.ID)
	finishVoiceRecapWork(t, app, d)
	u := &d.memoryUI.recap
	_, _, text := memoryWidgetTree(t, d)
	if !u.complete || !u.partial || u.processed != 51 || u.skipped != 50 || len(u.parts) != 1 || u.parts[0].first != 51 || u.parts[0].last != 51 || strings.Contains(text, assistant.InsufficientEvidenceText) || strings.Contains(text, "All 51 saved passages") || !strings.Contains(text, "could not summarize 50 saved passages") {
		t.Fatal("contentless prefix hid later speech or falsely claimed full summary coverage", u.status, u.processed, u.skipped)
	}
}

func TestFinishedVoiceRecapInvalidOutputDoesNotReportModelUnavailable(t *testing.T) {
	c, _, model, meeting := summaryFixture(t, 3)
	model.invalidCitation = true
	app := &meetingOperationTestApp{posted: make(chan func(), 128)}
	d := &desktop{app: app, client: c, page: "Ask", tokens: themePalette(false)}
	d.meetingUI.live = MeetingLiveView{State: "completed", Meeting: meeting}
	d.openFinishedVoiceRecap(meeting.ID)
	finishVoiceRecapWork(t, app, d)
	_, buttons, _ := memoryWidgetTree(t, d)
	clickMemoryButton(t, buttons, "Show transcript")
	_, _, text := memoryWidgetTree(t, d)
	if len(d.memoryUI.recap.parts) != 0 || d.memoryUI.recap.complete || !strings.Contains(text, "unverifiable recap for 3 saved passages") || strings.Contains(text, "unavailable with the current model") || !strings.Contains(text, "Saved decision 0") || model.calls != 2 {
		t.Fatal("invalid generated citation was hidden as provider unavailability", d.memoryUI.recap.status)
	}
}

func TestFinishedVoiceRecapContinuesAfterInvalidPrefixWithoutPublishingIt(t *testing.T) {
	c, _, model, meeting := summaryFixture(t, 51)
	model.invalidCitationCalls = 2 // Initial response and bounded repair both fail.
	meeting.RecordCount = 51
	app := &meetingOperationTestApp{posted: make(chan func(), 128)}
	d := &desktop{app: app, client: c, page: "Ask", tokens: themePalette(false)}
	d.meetingUI.live = MeetingLiveView{State: "completed", Meeting: meeting}
	d.openFinishedVoiceRecap(meeting.ID)
	finishVoiceRecapWork(t, app, d)
	u := &d.memoryUI.recap
	_, _, text := memoryWidgetTree(t, d)
	if !u.complete || !u.partial || u.processed != 51 || u.skipped != 50 || u.invalid != 50 || len(u.parts) != 1 || u.parts[0].first != 51 || u.parts[0].last != 51 || strings.Contains(text, assistant.InsufficientEvidenceText) || strings.Contains(text, "All 51 saved passages") || !strings.Contains(text, "unverifiable recap for 50 saved passages") {
		t.Fatal("invalid prefix hid later speech, leaked prose or claimed full coverage", u.status, u.processed, u.skipped, u.invalid)
	}
}

func TestMeetingSummaryFailureCopyKeepsReasonsDistinct(t *testing.T) {
	for _, scenario := range []struct {
		err      error
		contains string
	}{
		{assistant.ErrAskUnavailable, "unavailable"},
		{model.ErrAppleUnavailable, "unavailable"},
		{assistant.ErrSummaryInsufficientEvidence, "understandable content"},
		{assistant.ErrAnswer, "could not be verified"},
		{assistant.ErrGeneratorResponse, "could not be verified"},
		{model.ErrAppleResponse, "could not be verified"},
		{assistant.ErrTokenBudget, "available context"},
		{model.ErrAppleBudget, "available context"},
		{model.ErrAppleRefused, "declined"},
	} {
		if copy := meetingSummaryErrorCopy(scenario.err, ProviderApple); !strings.Contains(copy, scenario.contains) {
			t.Fatal("summary failure reason was collapsed", scenario.err, copy)
		}
	}
}

func TestFinishedVoiceRecapPreservesInterruptedMarkdownDraft(t *testing.T) {
	c, _, _, meeting := summaryFixture(t, 1)
	app := &meetingOperationTestApp{posted: make(chan func(), 128)}
	d := &desktop{app: app, client: c, page: "Ask", tokens: themePalette(false)}
	d.noteUI.open, d.noteUI.draft = true, "Unfinished private Markdown draft"
	d.meetingUI.live = MeetingLiveView{State: "completed", Meeting: meeting}
	d.openFinishedVoiceRecap(meeting.ID)
	finishVoiceRecapWork(t, app, d)
	_, buttons, _ := memoryWidgetTree(t, d)
	clickMemoryButton(t, buttons, "Recap voice note")
	finishVoiceRecapWork(t, app, d)
	if !d.memoryUI.recap.resumeEditor {
		t.Fatal("explicit recap retry lost interrupted editor state")
	}
	d.activateSidebarPage("Ask")
	if !d.noteUI.open || d.noteUI.draft != "Unfinished private Markdown draft" {
		t.Fatal("recap navigation lost the interrupted editor draft")
	}
	// A deliberate New note action while the recap suspends that editor must
	// also respect the ordinary dirty-draft confirmation.
	d.noteUI.open, d.memoryUI.recap.resumeEditor = false, true
	d.newNoteDocument()
	if !d.noteUI.open || !d.noteUI.discardPending || d.noteUI.draft != "Unfinished private Markdown draft" {
		t.Fatal("new-note action discarded the interrupted draft")
	}
}
