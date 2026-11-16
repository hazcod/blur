package client

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/golang-gui/goui/core/geometry"
	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/platform/events"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/deletion"
	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/notes"
)

func TestUnifiedMemoryBrowseIncludesVoiceAndNotesWithoutInference(t *testing.T) {
	c, store, model, meeting := summaryFixture(t, 25)
	ctx := context.Background()
	note, err := capture.SanitizeObservation("sh.blur.notes", "local", "Remember the review checklist", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservation(ctx, note); err != nil {
		t.Fatal(err)
	}
	page, err := c.browseSavedMemory(ctx, memoryBrowseCursor{})
	if err != nil {
		t.Fatal(err)
	}
	seenVoice, seenNote := false, false
	for passes := 0; ; passes++ {
		for _, item := range page.items {
			if item.meeting != nil {
				if item.meeting.ID != meeting.ID || item.meeting.RecordCount != 25 || item.SourceID != "" {
					t.Fatal("voice entry invented a citation or lost its saved metadata", item)
				}
				seenVoice = true
			} else if item.SourceID == note.ContentID() {
				seenNote = item.Text == note.Text() && sourceKind(item.SearchHit) == "Note"
			} else if item.AppID == "sh.blur.meetings" {
				t.Fatal("voice passages duplicated the grouped entry")
			}
		}
		if !page.more {
			break
		}
		if passes > 8 {
			t.Fatal("saved-memory cursor did not finish")
		}
		page, err = c.browseSavedMemory(ctx, page.next)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !seenVoice || !seenNote {
		t.Fatalf("saved entries missing: voice=%t note=%t", seenVoice, seenNote)
	}
	model.mu.Lock()
	calls := model.calls
	model.mu.Unlock()
	if calls != 0 {
		t.Fatal("plain browsing invoked the answer model")
	}
}

func TestSavedMemoryRowsUseReadableAppNames(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	d := &desktop{app: &quickDashboardApp{}, client: c}
	for _, test := range []struct {
		appID string
		name  string
	}{{"com.apple.mail", "Mail"}, {"com.example.editor", "Editor"}} {
		text := mountedLibraryText(t, d.memorySourceRow(SearchHit{AppID: test.appID, Text: "Review the rollback plan."}))
		if !strings.Contains(text, test.name) || strings.Contains(text, test.appID) {
			t.Errorf("source row for %q did not use readable app name %q:\n%s", test.appID, test.name, text)
		}
	}
}

func TestUnifiedMemoryBrowsePagesEveryDirectoryWithoutRepeatingFinishedOnes(t *testing.T) {
	f := newControllerFixture(t)
	ctx := context.Background()
	for i := 0; i < 15; i++ {
		meeting := meetings.Meeting{ID: fmt.Sprintf("browse-%d", i), Title: fmt.Sprintf("Voice note %d", i), Locale: "en-US", StartedAt: f.now.Add(time.Duration(i) * time.Minute), State: meetings.Ready, Tracks: []string{"microphone"}, Revision: 1}
		if err := f.store.PutMeeting(ctx, meeting, 0); err != nil {
			t.Fatal(err)
		}
		note, err := capture.SanitizeObservation("sh.blur.notes", "local", fmt.Sprintf("Saved personal note %d", i), 1024)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.PutObservation(ctx, note); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	var after memoryBrowseCursor
	for n := 0; ; n++ {
		page, err := f.c.browseSavedMemory(ctx, after)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.items {
			id := item.SourceID
			if item.meeting != nil {
				id = item.meeting.ID
			}
			if seen[id] {
				t.Fatal("saved directory restarted", id)
			}
			seen[id] = true
		}
		if !page.more {
			break
		}
		if page.next == after || n > 8 {
			t.Fatal("saved-memory paging made no progress")
		}
		after = page.next
	}
	if len(seen) != 30 {
		t.Fatalf("browsed %d saved entries, want 30", len(seen))
	}
	if f.c.MeetingLive().AudioRecording {
		t.Fatal("browsing opened recording")
	}
}

func TestUnifiedMemoryBrowseShowsNewNotesAndRecentCapturesBeforeOldDirectories(t *testing.T) {
	f := newControllerFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Add(-time.Second)
	for i := 0; i < 30; i++ {
		if _, err := f.store.CreateNoteDocument(ctx, fmt.Sprintf("Old note %d", i), now.Add(-time.Duration(90+i)*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		o, err := capture.SanitizeObservation("com.example.App", "example.org", fmt.Sprintf("Old capture %d", i), 1024)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.PutObservationAt(ctx, o, now.Add(-time.Duration(90+i)*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	newNote, err := f.store.CreateNoteDocument(ctx, "Just saved note belongs near the top.", now)
	if err != nil {
		t.Fatal(err)
	}
	recent, err := capture.SanitizeObservation("com.example.App", "example.org", "Just captured current document", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.PutObservationAt(ctx, recent, now); err != nil {
		t.Fatal(err)
	}
	page, err := f.c.browseSavedMemory(ctx, memoryBrowseCursor{})
	if err != nil {
		t.Fatal(err)
	}
	seenNote, seenCapture := false, false
	for _, item := range page.items {
		seenNote = seenNote || item.SourceID == newNote.SourceID
		seenCapture = seenCapture || item.SourceID == recent.ContentID()
	}
	if !seenNote || !seenCapture {
		t.Fatal("newest saved items were buried behind HMAC/old capture directories", seenNote, seenCapture)
	}
}

func TestUnifiedMemorySavedNoteRevealRefreshesActualSourceWithoutChangingDraftOrPopup(t *testing.T) {
	c, store, _ := todayTaskClient(t)
	app := &meetingOperationTestApp{posted: make(chan func(), 64)}
	d := &desktop{app: app, client: c, page: "Ask", askQuestion: "unsent topic", quickInput: "popup draft", memoryUI: memoryViewState{loaded: true}}
	result, err := c.AddNote("New source visible in Ask after a save")
	if err != nil {
		t.Fatal(err)
	}
	d.revealSavedMemorySource(result.SourceID)
	app.finish(t)
	if d.memoryUI.loaded || d.memoryUI.revealedSource == nil || d.memoryUI.revealedSource.SourceID != result.SourceID || d.askQuestion != "unsent topic" || d.quickInput != "popup draft" || d.quickPanelPinned {
		t.Fatal("saved note reveal changed draft/focus or failed to invalidate browser")
	}
	if source, found, err := store.GetObservation(context.Background(), d.memoryUI.revealedSource.SourceID); err != nil || !found || source.Text() != d.memoryUI.revealedSource.Text {
		t.Fatal("saved note reveal invented source content", err)
	}
	d.askQuestion = ""
	d.navigatePage("Ask")
	for d.memoryUI.loading {
		app.finish(t)
	}
	_, _, text := memoryWidgetTree(t, d)
	if !strings.Contains(text, "New source visible in Ask after a save") {
		t.Fatal("saved source did not appear on next Ask visit", text)
	}
	page, err := store.ListNoteDocumentsPage(context.Background(), notes.DocumentPageRequest{Limit: 12, MaxBytes: MaxSearchBytes})
	if err != nil || len(page.Documents) != 1 || page.Documents[0].SourceID != result.SourceID {
		t.Fatal("saved reveal diverged from current note directory", err)
	}
}

func memoryWidgetTree(t *testing.T, d *desktop) (gui.Widget, map[string][]*gui.Button, string) {
	t.Helper()
	w := mountVoicePopupView(t, d.askView())
	buttons := map[string][]*gui.Button{}
	var text strings.Builder
	var walk func(gui.Widget)
	walk = func(w gui.Widget) {
		if label, ok := w.(*gui.Label); ok {
			text.WriteString(label.Text())
			text.WriteByte('\n')
		}
		if button, ok := w.(*gui.Button); ok {
			if label, ok := button.Child().(*gui.Label); ok {
				buttons[label.Text()] = append(buttons[label.Text()], button)
			}
		}
		for _, child := range w.Children() {
			walk(child)
		}
	}
	walk(w)
	return w, buttons, text.String()
}

type memoryClickHost struct{ widget gui.Widget }

func (h *memoryClickHost) Widget() gui.Widget             { return h.widget }
func (*memoryClickHost) FocusedWidget() gui.Widget        { return nil }
func (*memoryClickHost) SetFocusedWidget(gui.Widget) bool { return false }

func clickMemoryButton(t *testing.T, buttons map[string][]*gui.Button, label string) {
	t.Helper()
	if len(buttons[label]) == 0 {
		t.Fatalf("missing mounted action %q", label)
	}
	button := buttons[label][0]
	button.Arrange(geometry.Rect(0, 0, 300, 36))
	host := &memoryClickHost{widget: button}
	dispatch := &gui.EventDispatcher{}
	for _, event := range []events.PointerEvent{
		{EventType: events.PointerDown, Position: geometry.Point{X: 10, Y: 10}, Button: events.PointerButtonLeft, Buttons: events.PointerButtonLeftDown},
		{EventType: events.PointerUp, Position: geometry.Point{X: 10, Y: 10}, Button: events.PointerButtonLeft},
	} {
		if err := dispatch.DispatchEvent(host, event); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUnifiedMemoryMountedVoiceEntryOpensPagedTranscriptAndReturns(t *testing.T) {
	c, _, model, _ := summaryFixture(t, 25)
	app := &meetingOperationTestApp{posted: make(chan func(), 16)}
	d := &desktop{app: app, client: c, page: "Ask", tokens: themePalette(false)}
	d.loadMemoryBrowse(memoryBrowseCursor{})
	app.finish(t)
	w, buttons, text := memoryWidgetTree(t, d)
	if !strings.Contains(text, "Private launch planning") {
		t.Fatal("saved voice entry is not visible before entering a query", text)
	}
	inputs, microphones := 0, 0
	var walk func(gui.Widget)
	walk = func(w gui.Widget) {
		if _, ok := w.(*gui.TextInput); ok {
			inputs++
		}
		if _, ok := w.(*gui.DrawingArea); ok {
			microphones++
		}
		for _, child := range w.Children() {
			walk(child)
		}
	}
	walk(w)
	if inputs != 1 || microphones != 1 {
		t.Fatalf("unified composer inputs=%d voice markers=%d", inputs, microphones)
	}
	clickMemoryButton(t, buttons, "Read voice note")
	app.finish(t)
	_, buttons, text = memoryWidgetTree(t, d)
	if strings.Contains(text, "Saved decision 0:") || len(buttons["Show transcript"]) != 1 {
		t.Fatal("saved voice detail did not default to a collapsed transcript", text)
	}
	clickMemoryButton(t, buttons, "Show transcript")
	_, buttons, text = memoryWidgetTree(t, d)
	if !d.memoryUI.meetingDetail || !strings.Contains(text, "Saved decision 0:") || !strings.Contains(text, "Saved decision 19:") || strings.Contains(text, "Saved decision 20:") {
		t.Fatal("saved voice detail is not the bounded first transcript page", text)
	}
	clickMemoryButton(t, buttons, "Later")
	app.finish(t)
	_, _, text = memoryWidgetTree(t, d)
	if !strings.Contains(text, "Saved decision 24:") || strings.Contains(text, "Saved decision 0:") {
		t.Fatal("transcript paging lost saved words", text)
	}
	d.activateSidebarPage("Ask")
	_, _, text = memoryWidgetTree(t, d)
	if d.memoryUI.meetingDetail || !strings.Contains(text, "Saved memory") {
		t.Fatal("voice detail did not return to unified results")
	}
	model.mu.Lock()
	calls := model.calls
	model.mu.Unlock()
	if calls != 0 || c.MeetingLive().AudioRecording {
		t.Fatal("reading saved voice text started model or recording work")
	}
}

func TestUnifiedMemoryVoicePhraseSearchUsesSavedTranscriptSource(t *testing.T) {
	f := newControllerFixture(t)
	id := f.start(t)
	const phrase = "The rollback owner is Mira."
	f.audio.stream.emit(controllerState(id, 1, "running"), controllerWords(id, 1, "unified-final", phrase, true))
	waitController(t, f.c, func(v MeetingLiveView) bool { return len(v.Tail) == 1 })
	if err := f.c.ControlMeeting(context.Background(), "stop"); err != nil {
		t.Fatal(err)
	}
	waitController(t, f.c, func(v MeetingLiveView) bool { return v.State == "completed" })
	saved, err := f.c.MeetingTranscript(context.Background(), id, 0)
	if err != nil || len(saved.Records) != 1 {
		t.Fatal("fixture did not save transcript", err)
	}
	app := &meetingOperationTestApp{posted: make(chan func(), 16)}
	d := &desktop{app: app, client: f.c, page: "Ask", askQuestion: "rollback owner", tokens: themePalette(false)}
	d.submitMemoryQuery()
	finishMemoryEntry(t, app, d)
	if d.askLoading || d.askResult != nil || len(d.searchHits) != 1 || d.searchHits[0].SourceID != saved.Records[0].SourceID {
		t.Fatal("topic query did not preserve the saved transcript identity", d.status)
	}
	_, _, text := memoryWidgetTree(t, d)
	if !strings.Contains(text, phrase) || !strings.Contains(text, "Voice note") || !strings.Contains(text, "Matching sources") {
		t.Fatal("voice transcript match is not readable in unified Ask", text)
	}
	for _, path := range []string{"search", "answer evidence"} {
		if path == "answer evidence" {
			d.memoryUI.meetingDetail = false
			d.askQuestion = "Who is Mira?"
			d.submitMemoryQuery()
			finishMemoryEntry(t, app, d)
			if d.askResult == nil || len(d.askResult.Sources) != 1 {
				t.Fatal("real voice answer evidence missing", d.status)
			}
		}
		_, buttons, _ := memoryWidgetTree(t, d)
		clickMemoryButton(t, buttons, "Read voice note")
		for d.meetingUI.reading {
			app.finish(t)
		} // Membership resolution and saved transcript page.
		_, buttons, _ = memoryWidgetTree(t, d)
		clickMemoryButton(t, buttons, "Show transcript")
		_, _, text = memoryWidgetTree(t, d)
		if !d.memoryUI.meetingDetail || d.meetingUI.selected != id || !strings.Contains(text, phrase) {
			t.Fatal("voice source did not open its genuine saved session", path, text)
		}
		d.memoryUI.meetingDetail = false
	}
}

func TestUnifiedMemoryInvalidationDropsSavedTextAndLateBrowseReceipt(t *testing.T) {
	c, _, _, _ := summaryFixture(t, 1)
	app := &meetingOperationTestApp{posted: make(chan func(), 16)}
	d := &desktop{app: app, client: c, page: "Ask"}
	d.loadMemoryBrowse(memoryBrowseCursor{})
	var receipt func()
	select {
	case receipt = <-app.posted:
	case <-time.After(5 * time.Second):
		t.Fatal("browse did not finish")
	}
	d.invalidateMemoryViews()
	receipt()
	if d.memoryUI.loaded || len(d.memoryUI.page.items) != 0 || d.memoryUI.meetingDetail {
		t.Fatal("late browse receipt restored invalidated memory")
	}
}

func TestUnifiedMemoryMountedVoiceDeletionRefreshesSavedResults(t *testing.T) {
	c, store, _, meeting := summaryFixture(t, 1)
	deleter, err := deletion.NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.WithDeletion(deleter)
	app := &meetingOperationTestApp{posted: make(chan func(), 16)}
	d := &desktop{app: app, client: c, page: "Ask", tokens: themePalette(false)}
	d.loadMemoryBrowse(memoryBrowseCursor{})
	app.finish(t)
	d.openMemoryMeeting(meeting)
	app.finish(t)
	_, buttons, _ := memoryWidgetTree(t, d)
	clickMemoryButton(t, buttons, "Delete voice note…")
	_, buttons, _ = memoryWidgetTree(t, d)
	clickMemoryButton(t, buttons, "Delete voice note")
	app.finish(t) // Deletion receipt starts the saved-memory refresh.
	app.finish(t)
	_, _, text := memoryWidgetTree(t, d)
	if _, err := c.browseSavedMemory(context.Background(), memoryBrowseCursor{}); err != nil {
		t.Fatalf("saved directory could not refresh after deletion: %v", err)
	}
	if d.memoryUI.meetingDetail || !d.memoryUI.loaded || strings.Contains(text, meeting.Title) || len(d.memoryUI.page.items) != 0 {
		t.Fatal("deleted voice note remained in the saved browser", text)
	}
	if _, found, err := store.GetMeeting(context.Background(), meeting.ID); err != nil || found {
		t.Fatal("voice deletion did not erase the saved session", err)
	}
}
