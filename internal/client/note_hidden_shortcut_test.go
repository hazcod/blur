package client

import "testing"

func TestNoteSaveShortcutCannotSaveDraftHiddenByAnotherPage(t *testing.T) {
	client := NewClient(&testData{})
	t.Cleanup(client.Close)
	app := &meetingOperationTestApp{posted: make(chan func(), 1)}
	d := &desktop{client: client, app: app, page: "Settings", noteUI: noteDocumentViewState{open: true, draft: "Do not save a hidden editor draft."}}
	d.saveNoteDocument()
	if d.noteUI.saving || d.noteUI.saveToken != nil || d.noteUI.draft != "Do not save a hidden editor draft." {
		t.Fatal("a hidden editor handled Save")
	}
}
