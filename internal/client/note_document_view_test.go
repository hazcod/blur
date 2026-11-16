package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/platform/events"
	"github.com/hazcod/blur/internal/deletion"
)

func finishNoteDocumentWork(t *testing.T, app *meetingOperationTestApp, d *desktop) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for d.noteUI.loading || d.noteUI.saving || d.noteUI.transferring {
		select {
		case receipt := <-app.posted:
			receipt()
		case <-deadline.C:
			t.Fatal("note editor operation stalled", d.noteUI.status)
		}
	}
}

// Mirror the desktop's synchronous UI invalidator without a native app. The
// worker waits until the mounted test's UI queue has hidden the old plaintext.
func installNoteDocumentTestInvalidator(c *Client, app *meetingOperationTestApp, d *desktop) {
	c.SetMemoryInvalidator(func() {
		done := make(chan struct{})
		app.Post(func() { d.invalidateMemoryViews(); close(done) })
		<-done
	})
}

func runNoteDocumentMutation(t *testing.T, app *meetingOperationTestApp, mutation func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- mutation() }()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case receipt := <-app.posted:
			receipt()
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			return
		case <-deadline.C:
			t.Fatal("note mutation stalled")
		}
	}
}

func mountedNoteEditor(t *testing.T, d *desktop) (gui.Widget, *gui.TextView, map[string][]*gui.Button) {
	t.Helper()
	root, buttons, _ := memoryWidgetTree(t, d)
	var editor *gui.TextView
	var walk func(gui.Widget)
	walk = func(w gui.Widget) {
		if text, ok := w.(*gui.TextView); ok {
			editor = text
		}
		for _, child := range w.Children() {
			walk(child)
		}
	}
	walk(root)
	if editor == nil {
		t.Fatal("real multiline Markdown editor was not mounted")
	}
	return root, editor, buttons
}

func TestNoteDocumentEditorMountedCreateAndEditUseStableIdentityAndOwnReceipt(t *testing.T) {
	c, store, _ := todayTaskClient(t)
	app := &meetingOperationTestApp{posted: make(chan func(), 64)}
	d := &desktop{app: app, client: c, page: "Ask", quickInput: "popup draft", tokens: themePalette(false)}
	installNoteDocumentTestInvalidator(c, app, d)
	askRoot, _, _ := memoryWidgetTree(t, d)
	var composer *gui.TextInput
	var findComposer func(gui.Widget)
	findComposer = func(w gui.Widget) {
		if input, ok := w.(*gui.TextInput); ok {
			composer = input
		}
		for _, child := range w.Children() {
			findComposer(child)
		}
	}
	findComposer(askRoot)
	if composer == nil {
		t.Fatal("Ask did not mount its note composer")
	}
	const first = "Release plan: review the build and keep source links."
	composer.SetText(first)
	askRoot.AddEventController(d.newMemoryKeyController())
	handled := false
	if err := new(gui.EventDispatcher).DispatchEvent(&memoryKeyHost{root: askRoot, focused: composer}, events.KeyEvent{EventType: events.KeyDown, Key: events.KeyS, Modifiers: events.ModifierCommand, Handled: &handled}); err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("Ask Command-S did not create the note")
	}
	d.askQuestion = ""
	finishMemoryEntry(t, app, d)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for d.memoryUI.revealedSource == nil {
		select {
		case receipt := <-app.posted:
			receipt()
		case <-deadline.C:
			t.Fatal("saved note was not revealed in Ask", d.status)
		}
	}
	_, actions, _ := memoryWidgetTree(t, d)
	clickMemoryButton(t, actions, "Edit note")
	finishNoteDocumentWork(t, app, d)
	initial := d.noteUI.document
	if !d.noteUI.open || initial.ID == "" || initial.Revision != 1 || initial.Text != first || d.noteDocumentDirty() {
		t.Fatal("new Markdown note did not retain committed source", d.noteUI.status, initial)
	}
	statusText := mountedLibraryText(t, d.noteDocumentView())
	if !strings.Contains(statusText, "Last saved ") || strings.Contains(statusText, "revision") {
		t.Fatalf("note editor exposed storage revision instead of a human save status:\n%s", statusText)
	}
	root, editor, _ := mountedNoteEditor(t, d)
	const revised = "# Release plan\n\n- Review the signed build\n- Keep source links\n"
	editor.Model().SetText(revised)
	root.AddEventController(d.newMemoryKeyController())
	handled = false
	if err := new(gui.EventDispatcher).DispatchEvent(&memoryKeyHost{root: root, focused: editor}, events.KeyEvent{EventType: events.KeyDown, Key: events.KeyS, Modifiers: events.ModifierCommand, Handled: &handled}); err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("editor Command-S was not handled")
	}
	finishNoteDocumentWork(t, app, d)
	saved := d.noteUI.document
	if !d.noteUI.open || saved.ID != initial.ID || saved.Revision != 2 || saved.SourceID == initial.SourceID || saved.Text != revised || d.noteUI.draft != revised || d.noteDocumentDirty() {
		t.Fatal("own save invalidation lost the editor receipt", d.noteUI.status, saved)
	}
	current, err := c.NoteDocument(context.Background(), initial.SourceID)
	if err != nil || current.SourceID != saved.SourceID || current.Text != revised {
		t.Fatal("old source did not resolve stable current note", err, current)
	}
	if source, found, err := store.GetObservation(context.Background(), saved.SourceID); err != nil || !found || source.Text() != revised {
		t.Fatal("edited note did not retain genuine saved bytes", err)
	}
	if d.quickInput != "popup draft" || d.quickPanelPinned {
		t.Fatal("note editor shortcut changed popup draft or focus")
	}
	d.closeNoteDocument()
	d.askQuestion = "Release plan"
	d.searchHits = []SearchHit{{SourceID: initial.SourceID, AppID: "sh.blur.notes", Text: first}}
	_, actions, _ = memoryWidgetTree(t, d)
	clickMemoryButton(t, actions, "Edit note")
	finishNoteDocumentWork(t, app, d)
	if d.noteUI.document.ID != saved.ID || d.noteUI.draft != revised {
		t.Fatal("unified source action did not open the current stable note")
	}
}

func TestNoteDocumentConflictAndBackKeepDraftUntilExplicitDiscard(t *testing.T) {
	c, store, _ := todayTaskClient(t)
	document, err := c.CreateNoteDocument(context.Background(), "# Original\nSaved text")
	if err != nil {
		t.Fatal(err)
	}
	app := &meetingOperationTestApp{posted: make(chan func(), 64)}
	d := &desktop{app: app, client: c, page: "Ask", tokens: themePalette(false)}
	d.openNoteDocument(document.SourceID)
	finishNoteDocumentWork(t, app, d)
	_, editor, buttons := mountedNoteEditor(t, d)
	const draft = "# My unsaved Markdown\nDo not lose this draft."
	editor.Model().SetText(draft)
	if _, err := store.UpdateNoteDocument(context.Background(), document.ID, document.Revision, "# Changed elsewhere\nCurrent saved text", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	clickMemoryButton(t, buttons, "Save note")
	finishNoteDocumentWork(t, app, d)
	if d.noteUI.draft != draft || !d.noteDocumentDirty() || !strings.Contains(d.noteUI.status, "changed in another view") {
		t.Fatal("compare-and-save conflict erased the draft", d.noteUI.status)
	}
	_, _, buttons = mountedNoteEditor(t, d)
	clickMemoryButton(t, buttons, "Back to Ask")
	_, _, buttons = mountedNoteEditor(t, d)
	if !d.noteUI.discardPending || !d.noteUI.open || d.noteUI.draft != draft {
		t.Fatal("Back silently discarded the Markdown draft")
	}
	clickMemoryButton(t, buttons, "Keep editing")
	if d.noteUI.discardPending || d.noteUI.draft != draft {
		t.Fatal("Keep editing lost the draft")
	}
	_, _, buttons = mountedNoteEditor(t, d)
	clickMemoryButton(t, buttons, "Back to Ask")
	_, _, buttons = mountedNoteEditor(t, d)
	clickMemoryButton(t, buttons, "Discard changes")
	if d.noteUI.open || d.noteUI.draft != "" {
		t.Fatal("explicit discard did not close the draft")
	}
	current, err := c.NoteDocument(context.Background(), document.ID)
	if err != nil || !strings.Contains(current.Text, "Changed elsewhere") {
		t.Fatal("discard altered the saved note", err)
	}
}

func TestNoteDocumentCopyAndExportUseOnlyCommittedMarkdown(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	document, err := c.CreateNoteDocument(context.Background(), "# Roadmap\n\n- First milestone\n\n[Source](https://example.org)\n")
	if err != nil {
		t.Fatal(err)
	}
	app := &meetingOperationTestApp{posted: make(chan func(), 64)}
	d := &desktop{app: app, client: c, page: "Ask", tokens: themePalette(false)}
	d.openNoteDocument(document.SourceID)
	finishNoteDocumentWork(t, app, d)
	var copied, filename string
	path := filepath.Join(t.TempDir(), "export.md")
	d.noteUI.copyText = func(text string) error { copied = text; return nil }
	d.noteUI.chooseExport = func(name string) (string, error) { filename = name; return path, nil }
	_, editor, buttons := mountedNoteEditor(t, d)
	clickMemoryButton(t, buttons, "Copy Markdown")
	if copied != "" || !d.noteUI.transferring {
		t.Fatal("committed Markdown was read on the UI thread")
	}
	finishNoteDocumentWork(t, app, d)
	clickMemoryButton(t, buttons, "Export .md")
	finishNoteDocumentWork(t, app, d)
	exported, err := os.ReadFile(path)
	if err != nil || string(exported) != document.Text || copied != document.Text || filename != "Roadmap.md" {
		t.Fatal("copy/export changed committed Markdown", err, filename, copied, string(exported))
	}
	editor.Model().SetText(document.Text + "\nUNSAVED_PRIVATE_DRAFT")
	_, _, buttons = mountedNoteEditor(t, d)
	if d.noteDocumentSavedText() != "" {
		t.Fatal("dirty draft exposed committed-note transfer")
	}
	// The minimal mounting harness does not apply shared view properties such
	// as Enabled. Exercise the actual guarded callbacks as well as the editor.
	clickMemoryButton(t, buttons, "Copy Markdown")
	clickMemoryButton(t, buttons, "Export .md")
	if copied != document.Text {
		t.Fatal("dirty draft reached the clipboard")
	}
	exported, err = os.ReadFile(path)
	if err != nil || strings.Contains(string(exported), "UNSAVED_PRIVATE_DRAFT") {
		t.Fatal("unsaved draft was exported", err)
	}
}

func TestNoteDocumentErasureRejectsLateReadAndOwnSaveReceipt(t *testing.T) {
	c, store, _ := todayTaskClient(t)
	deleter, err := deletion.NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.WithDeletion(deleter)
	document, err := c.CreateNoteDocument(context.Background(), "Private note before deletion")
	if err != nil {
		t.Fatal(err)
	}
	app := &meetingOperationTestApp{posted: make(chan func(), 64)}
	d := &desktop{app: app, client: c, page: "Ask", tokens: themePalette(false)}
	installNoteDocumentTestInvalidator(c, app, d)
	d.openNoteDocument(document.SourceID)
	var readReceipt func()
	select {
	case readReceipt = <-app.posted:
	case <-time.After(5 * time.Second):
		t.Fatal("note read did not finish")
	}
	runNoteDocumentMutation(t, app, func() error { return c.DeleteNoteDocument(context.Background(), document.ID, document.Revision) })
	readReceipt()
	if d.noteUI.open || d.noteUI.draft != "" || d.noteUI.document.Text != "" {
		t.Fatal("late read restored erased note plaintext")
	}
	document, err = c.CreateNoteDocument(context.Background(), "Second private note")
	if err != nil {
		t.Fatal(err)
	}
	d.openNoteDocument(document.SourceID)
	finishNoteDocumentWork(t, app, d)
	_, editor, buttons := mountedNoteEditor(t, d)
	editor.Model().SetText("Second private note with committed edit")
	clickMemoryButton(t, buttons, "Save note")
	// Successful updates synchronously invalidate the old editor before the
	// worker can prepare its fresh-epoch receipt.
	app.finish(t)
	var saveReceipt func()
	select {
	case saveReceipt = <-app.posted:
	case <-time.After(5 * time.Second):
		t.Fatal("note save did not finish")
	}
	current, err := c.NoteDocument(context.Background(), document.ID)
	if err != nil || current.Revision != 2 {
		t.Fatal("fixture did not commit edit before pending receipt", err)
	}
	runNoteDocumentMutation(t, app, func() error { return c.DeleteNoteDocument(context.Background(), current.ID, current.Revision) })
	saveReceipt()
	if d.noteUI.open || d.noteUI.draft != "" || d.noteUI.document.Text != "" {
		t.Fatal("late own-save receipt restored erased note plaintext")
	}
}

func TestNoteDocumentExportRechecksAfterNativeDestinationChoice(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	document, err := c.CreateNoteDocument(context.Background(), "Do not export after erasure")
	if err != nil {
		t.Fatal(err)
	}
	app := &meetingOperationTestApp{posted: make(chan func(), 64)}
	d := &desktop{app: app, client: c, page: "Ask", tokens: themePalette(false)}
	c.SetMemoryInvalidator(d.invalidateMemoryViews)
	d.openNoteDocument(document.SourceID)
	finishNoteDocumentWork(t, app, d)
	path := filepath.Join(t.TempDir(), "blocked.md")
	d.noteUI.chooseExport = func(string) (string, error) { c.NotifyMemoryChanging(); c.NotifyMemoryStable(); return path, nil }
	d.exportNoteDocumentMarkdown()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("export published note bytes after its memory epoch retired", err)
	}
}

func TestNoteDocumentCopyDropsQueuedReceiptAfterErasure(t *testing.T) {
	c, store, _ := todayTaskClient(t)
	deleter, err := deletion.NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.WithDeletion(deleter)
	document, err := c.CreateNoteDocument(context.Background(), "Private Markdown")
	if err != nil {
		t.Fatal(err)
	}
	app := &meetingOperationTestApp{posted: make(chan func(), 64)}
	d := &desktop{app: app, client: c, page: "Ask", tokens: themePalette(false)}
	installNoteDocumentTestInvalidator(c, app, d)
	d.openNoteDocument(document.SourceID)
	finishNoteDocumentWork(t, app, d)
	copied := false
	d.noteUI.copyText = func(string) error { copied = true; return nil }
	d.copyNoteDocumentMarkdown()
	var receipt func()
	select {
	case receipt = <-app.posted:
	case <-time.After(5 * time.Second):
		t.Fatal("Markdown read did not finish")
	}
	runNoteDocumentMutation(t, app, func() error { return c.DeleteNoteDocument(context.Background(), document.ID, document.Revision) })
	receipt()
	if copied || d.noteUI.open || d.noteUI.draft != "" {
		t.Fatal("queued copy restored or copied erased Markdown")
	}
}

func TestNoteDocumentSlowExportStageAllowsMemoryAdmissionAndDeletion(t *testing.T) {
	c, store, _ := todayTaskClient(t)
	deleter, err := deletion.NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.WithDeletion(deleter)
	document, err := c.CreateNoteDocument(context.Background(), "Private Markdown must not replace destination after erasure")
	if err != nil {
		t.Fatal(err)
	}
	app := &meetingOperationTestApp{posted: make(chan func(), 64)}
	d := &desktop{app: app, client: c, page: "Ask", tokens: themePalette(false)}
	installNoteDocumentTestInvalidator(c, app, d)
	d.openNoteDocument(document.SourceID)
	finishNoteDocumentWork(t, app, d)
	path := filepath.Join(t.TempDir(), "existing.md")
	if err := os.WriteFile(path, []byte("Keep existing export"), 0600); err != nil {
		t.Fatal(err)
	}
	d.noteUI.chooseExport = func(string) (string, error) { return path, nil }
	staging, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	d.exportNoteDocumentMarkdownUsing(func(path, text string) (*stagedMarkdownExport, error) {
		staged, err := stageMarkdownExport(path, text)
		close(staging)
		<-release
		return staged, err
	})
	select {
	case <-staging:
	case <-time.After(5 * time.Second):
		t.Fatal("export never reached staging")
	}
	admitted := make(chan bool, 1)
	go func() { _, stable := c.BeginMemoryRead(); admitted <- stable }()
	select {
	case stable := <-admitted:
		if !stable {
			t.Fatal("staging changed the memory epoch")
		}
	case <-time.After(time.Second):
		t.Fatal("slow export staging blocked Ask admission")
	}
	// Deletion's NotifyMemoryChanging and synchronous UI invalidation must
	// complete even while destination staging remains blocked.
	runNoteDocumentMutation(t, app, func() error { return c.DeleteNoteDocument(context.Background(), document.ID, document.Revision) })
	close(release)
	app.finish(t)
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "Keep existing export" {
		t.Fatal("stale staged export replaced destination", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatal("stale export retained temporary plaintext", err)
	}
}
