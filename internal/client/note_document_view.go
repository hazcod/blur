package client

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/golang-gui/goui/layout"
	"github.com/golang-gui/goui/platform/events"
	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/notes"
)

type noteDocumentSaveToken byte

type noteDocumentViewState struct {
	open, loading, saving      bool
	transferring               bool
	transferRequest            uint64
	document                   notes.Document
	draft, sourceID, status    string
	request                    uint64
	readCancel                 context.CancelFunc
	saveToken                  *noteDocumentSaveToken
	discardPending, pendingNew bool
	pendingSource              string
	copyText                   func(string) error
	chooseExport               func(string) (string, error)
}

func (d *desktop) noteDocumentDirty() bool {
	u := &d.noteUI
	if u.document.ID == "" {
		return strings.TrimSpace(u.draft) != ""
	}
	return u.draft != u.document.Text
}

// Every mutation immediately removes editor plaintext. An in-flight save keeps
// only its opaque token, then re-reads its committed current revision before it
// may restore a document. External deletion can therefore win at either fence.
func (d *desktop) invalidateNoteDocumentView() {
	u := &d.noteUI
	u.request++
	u.transferRequest++
	u.transferring = false
	if u.readCancel != nil {
		u.readCancel()
		u.readCancel = nil
	}
	u.open, u.loading = false, false
	u.document = notes.Document{}
	u.draft, u.sourceID, u.status = "", "", ""
	u.discardPending, u.pendingNew, u.pendingSource = false, false, ""
}

// A new authored draft has no stored source for a memory mutation to erase.
// Keep that writing while retiring every read/transfer receipt. Stored note
// revisions still take the full invalidation path so deletion wins over late
// reads and saves.
func (d *desktop) invalidateNoteDocumentMemoryView() {
	u := &d.noteUI
	var draft string
	if u.open && !u.loading && !u.saving && u.document.ID == "" && u.sourceID == "" {
		draft = u.draft
	}
	d.invalidateNoteDocumentView()
	if strings.TrimSpace(draft) != "" {
		u.open, u.draft = true, draft
	}
}

func (d *desktop) newNoteDocument() {
	if d.noteUI.saving {
		return
	}
	d.clearRecapSources()
	if (d.noteUI.open || d.memoryUI.recap.resumeEditor) && d.noteDocumentDirty() {
		d.noteUI.open, d.memoryUI.recap.resumeEditor = true, false
		d.noteUI.discardPending, d.noteUI.pendingNew, d.noteUI.pendingSource = true, true, ""
		d.app.RequestUpdate()
		return
	}
	d.invalidateNoteDocumentView()
	d.noteUI.open = true
	d.noteUI.saveToken = nil
	d.page = "Ask"
	d.app.RequestUpdate()
}

func (d *desktop) openNoteDocument(sourceID string) {
	if d == nil || d.client == nil || sourceID == "" || d.noteUI.saving {
		return
	}
	d.clearRecapSources()
	u := &d.noteUI
	if (u.open || d.memoryUI.recap.resumeEditor) && d.noteDocumentDirty() {
		u.open, d.memoryUI.recap.resumeEditor = true, false
		u.discardPending, u.pendingNew, u.pendingSource = true, false, sourceID
		d.app.RequestUpdate()
		return
	}
	d.invalidateNoteDocumentView()
	u.open, u.loading, u.sourceID = true, true, sourceID
	u.saveToken = nil
	d.page = "Ask"
	epoch, stable := d.client.BeginMemoryRead()
	if !stable {
		u.loading = false
		u.status = "Memory is changing. Try opening the note again."
		d.app.RequestUpdate()
		return
	}
	request := u.request
	ctx, cancel := context.WithCancel(context.Background())
	u.readCancel = cancel
	d.app.RequestUpdate()
	go func() {
		document, err := d.client.NoteDocument(ctx, sourceID)
		cancel()
		d.app.Post(func() {
			if request != u.request || !u.open {
				return
			}
			u.loading, u.readCancel = false, nil
			if !d.client.PublishMemoryRead(epoch, func() {
				if err != nil {
					u.status = noteDocumentErrorCopy(err)
					return
				}
				u.document, u.draft, u.sourceID = document, document.Text, document.SourceID
			}) {
				d.invalidateNoteDocumentView()
			}
			d.app.RequestUpdate()
		})
	}()
}

func noteDocumentErrorCopy(err error) string {
	switch {
	case errors.Is(err, notes.ErrDocumentConflict):
		return "This note changed in another view. Your draft is kept. Reload the saved version before editing it again."
	case errors.Is(err, notes.ErrDocumentNotFound):
		return "This note is no longer available."
	case errors.Is(err, ErrMemoryChanging):
		return "Memory changed before this note could be saved. Your draft is kept."
	default:
		return "This note could not be saved or opened. Your draft is kept; try again."
	}
}

func (d *desktop) closeNoteDocument() {
	if d.noteUI.saving {
		return
	}
	if d.noteDocumentDirty() {
		d.noteUI.discardPending, d.noteUI.pendingNew, d.noteUI.pendingSource = true, false, ""
		d.app.RequestUpdate()
		return
	}
	d.invalidateNoteDocumentView()
	d.noteUI.saveToken = nil
	d.app.RequestUpdate()
}

func (d *desktop) discardNoteDocumentDraft() {
	newDocument, source := d.noteUI.pendingNew, d.noteUI.pendingSource
	d.invalidateNoteDocumentView()
	d.noteUI.saveToken = nil
	if newDocument {
		d.newNoteDocument()
	} else if source != "" {
		d.openNoteDocument(source)
	}
	d.app.RequestUpdate()
}

func (d *desktop) saveNoteDocument() {
	u := &d.noteUI
	if d.page != "Ask" || !u.open || u.loading || u.saving || !d.noteDocumentDirty() || strings.TrimSpace(u.draft) == "" || len(u.draft) > notes.MaxNoteBytes || d.client == nil {
		return
	}
	input, previous := u.draft, u.document
	token := new(noteDocumentSaveToken)
	u.saveToken, u.saving, u.status = token, true, "Saving note…"
	d.app.RequestUpdate()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), noteOperationTimeout)
		defer cancel()
		var saved notes.Document
		var err error
		if previous.ID == "" {
			saved, err = d.client.CreateNoteDocument(ctx, input)
		} else {
			saved, err = d.client.UpdateNoteDocument(ctx, previous.ID, previous.Revision, input)
		}
		var epoch uint64
		var current notes.Document
		if err == nil {
			var stable bool
			epoch, stable = d.client.BeginMemoryRead()
			if !stable {
				err = ErrMemoryChanging
			} else {
				current, err = d.client.NoteDocument(ctx, saved.ID)
				if err == nil && (current.SourceID != saved.SourceID || current.Revision != saved.Revision || current.Text != saved.Text) {
					err = ErrMemoryChanging
				}
			}
		}
		d.app.Post(func() {
			if u.saveToken != token {
				return
			}
			u.saving, u.saveToken = false, nil
			if err != nil {
				// Compare-and-save failures do not invalidate the editor; preserve
				// its exact draft. Erasure already hid it synchronously.
				if u.open {
					u.status = noteDocumentErrorCopy(err)
				}
				d.app.RequestUpdate()
				return
			}
			if !d.client.PublishMemoryRead(epoch, func() {
				u.open = true
				u.document, u.draft, u.sourceID = current, current.Text, current.SourceID
				u.status = "Saved."
				u.loading, u.discardPending = false, false
			}) {
				d.invalidateNoteDocumentView()
				d.app.RequestUpdate()
				return
			}
			d.revealSavedMemorySource(current.SourceID)
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) noteDocumentSavedText() string {
	u := &d.noteUI
	if !u.open || u.loading || u.saving || u.document.ID == "" || d.noteDocumentDirty() {
		return ""
	}
	return u.document.Text
}

// Reading encrypted Markdown and writing an export run outside the UI thread.
// Only the native clipboard and destination picker stay on the UI thread.
func (d *desktop) copyNoteDocumentMarkdown() {
	document, request, ok := d.beginNoteDocumentTransfer()
	if !ok {
		return
	}
	go func() {
		current, epoch, err := d.client.noteDocumentTransferSnapshot(document)
		d.app.Post(func() {
			if !d.noteDocumentTransferCurrent(request, document) {
				return
			}
			d.noteUI.transferring = false
			if err != nil {
				d.noteUI.status = noteDocumentErrorCopy(err)
				d.app.RequestUpdate()
				return
			}
			copyText := d.noteUI.copyText
			if copyText == nil {
				copyText = copyToPasteboard
			}
			if !d.client.PublishMemoryRead(epoch, func() { err = copyText(current.Text) }) {
				d.invalidateNoteDocumentView()
			} else if err != nil {
				d.noteUI.status = "The Markdown could not be copied."
			} else {
				d.noteUI.status = "Copied Markdown."
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) exportNoteDocumentMarkdown() {
	d.exportNoteDocumentMarkdownUsing(stageMarkdownExport)
}

func (d *desktop) exportNoteDocumentMarkdownUsing(stage func(string, string) (*stagedMarkdownExport, error)) {
	text := d.noteDocumentSavedText()
	if text == "" || d.noteUI.transferring {
		return
	}
	name := markdownNoteFilename(text)
	if choose := d.noteUI.chooseExport; choose != nil {
		path, err := choose(name)
		d.continueNoteDocumentMarkdownExport(path, err, stage)
		return
	}
	requestMarkdownExport(d.app, name, func(path string, err error) {
		d.continueNoteDocumentMarkdownExport(path, err, stage)
	})
}

// continueNoteDocumentMarkdownExport runs on the UI thread once the save
// location is known; platform pickers may deliver it from a callback.
func (d *desktop) continueNoteDocumentMarkdownExport(path string, err error, stage func(string, string) (*stagedMarkdownExport, error)) {
	if errors.Is(err, errMarkdownExportCancelled) {
		return
	}
	if err != nil {
		d.noteUI.status = "The Markdown file could not be exported. Try another location."
		d.app.RequestUpdate()
		return
	}
	// The picker can run a nested native event loop. Capture the saved revision
	// only after it returns, so intervening erasure cancels this transfer.
	document, request, ok := d.beginNoteDocumentTransfer()
	if !ok {
		return
	}
	go func() {
		current, _, err := d.client.noteDocumentTransferSnapshot(document)
		if err == nil {
			var staged *stagedMarkdownExport
			staged, err = stage(path, current.Text)
			if err == nil {
				// A slow destination must not hold memory admission while staging.
				// Re-read the current revision afterward, then publish only rename.
				var epoch uint64
				_, epoch, err = d.client.noteDocumentTransferSnapshot(current)
				if err == nil && !d.client.PublishMemoryRead(epoch, func() { err = staged.commit() }) {
					err = ErrMemoryChanging
				}
				staged.discard()
			}
		}
		d.app.Post(func() {
			if !d.noteDocumentTransferCurrent(request, document) {
				return
			}
			d.noteUI.transferring = false
			if errors.Is(err, ErrMemoryChanging) || errors.Is(err, notes.ErrDocumentNotFound) {
				d.invalidateNoteDocumentView()
			} else if err != nil {
				d.noteUI.status = "The Markdown file could not be exported. Try another location."
			} else {
				d.noteUI.status = "Exported Markdown."
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) beginNoteDocumentTransfer() (notes.Document, uint64, bool) {
	if d.noteDocumentSavedText() == "" || d.noteUI.transferring || d.client == nil {
		return notes.Document{}, 0, false
	}
	u := &d.noteUI
	u.transferRequest++
	u.transferring, u.status = true, "Reading saved Markdown…"
	d.app.RequestUpdate()
	return u.document, u.transferRequest, true
}

func (d *desktop) noteDocumentTransferCurrent(request uint64, document notes.Document) bool {
	u := &d.noteUI
	if request != u.transferRequest {
		return false
	}
	if !u.open || u.document.ID != document.ID || u.document.Revision != document.Revision || u.document.SourceID != document.SourceID || d.noteDocumentDirty() {
		u.transferring = false
		return false
	}
	return true
}

func (c *Client) noteDocumentTransferSnapshot(saved notes.Document) (notes.Document, uint64, error) {
	epoch, stable := c.BeginMemoryRead()
	if !stable {
		return notes.Document{}, 0, ErrMemoryChanging
	}
	ctx, cancel := context.WithTimeout(context.Background(), shortOperationTimeout)
	defer cancel()
	document, err := c.NoteDocument(ctx, saved.ID)
	if err != nil {
		return notes.Document{}, 0, err
	}
	if document.Revision != saved.Revision || document.SourceID != saved.SourceID || document.Text != saved.Text {
		return notes.Document{}, 0, notes.ErrDocumentConflict
	}
	return document, epoch, nil
}

func (d *desktop) handleNoteDocumentKey(event events.KeyEvent) bool {
	if d.page != "Ask" || !d.noteUI.open || event.Modifiers != events.ModifierCommand {
		return false
	}
	switch event.Key {
	case events.KeyS:
		d.saveNoteDocument()
		return true
	case events.KeyN:
		d.newNoteDocument()
		return true
	default:
		return false
	}
}

func (d *desktop) noteDocumentView() ui.View {
	u := &d.noteUI
	title := "Note"
	if u.document.ID == "" && !u.loading {
		title = "New note"
	}
	rows := []ui.View{ui.HBox(flatButton("Back to Ask").Enabled(!u.saving).OnClick(d.closeNoteDocument), wrapLabel(title).Style(styleSectionTitle).MainWeight(1)).Spacing(12).CrossAlign(layout.CrossCenter)}
	if u.loading {
		rows = append(rows, wrapLabel("Loading saved note…").Style(styleMeta))
	}
	if !u.document.UpdatedAt.IsZero() {
		rows = append(rows, wrapLabel("Last saved "+u.document.UpdatedAt.In(time.Local).Format("2 Jan 2006 at 15:04")).Style(styleMeta))
	}
	if u.status != "" {
		rows = append(rows, wrapLabel(u.status).Style(styleMeta))
	}
	if u.discardPending {
		rows = append(rows, wrapLabel("Discard unsaved changes? Your latest saved version will be kept.").Style(modern.MutedText), ui.HBox(
			flatButton("Keep editing").OnClick(func() { u.discardPending = false; u.pendingSource = ""; u.pendingNew = false; d.app.RequestUpdate() }),
			dangerButton("Discard changes").OnClick(d.discardNoteDocumentDraft),
		).Spacing(8))
	}
	rows = append(rows, ui.TextView().Text(u.draft).Placeholder("Write a note in Markdown…").WrapMode(ui.WrapWordChar).MinHeight(360).MaxHeight(960).Enabled(!u.loading && !u.saving).OnText(func(text string) { u.draft = text; u.discardPending = false; d.app.RequestUpdate() }))
	valid := !u.loading && !u.saving && d.noteDocumentDirty() && strings.TrimSpace(u.draft) != "" && len(u.draft) <= notes.MaxNoteBytes
	exportAvailable := d.noteDocumentSavedText() != "" && !u.transferring && (supportsMarkdownExport() || u.chooseExport != nil)
	rows = append(rows, ui.HBox(
		primaryButton("Save note").Enabled(valid).OnClick(d.saveNoteDocument),
		flatButton("Copy Markdown").Enabled(d.noteDocumentSavedText() != "" && !u.transferring).OnClick(d.copyNoteDocumentMarkdown),
		flatButton("Export .md").Enabled(exportAvailable).OnClick(d.exportNoteDocumentMarkdown),
	).Spacing(8))
	if d.noteDocumentDirty() {
		rows = append(rows, wrapLabel("Unsaved changes. Save to copy or export the current Markdown.").Style(styleMeta))
	}
	if len(u.draft) > notes.MaxNoteBytes {
		rows = append(rows, wrapLabel("This note is too large to save. Keep it under 1 MiB.").Style(styleMeta))
	}
	if u.document.ID != "" && u.status != "" && !u.saving {
		rows = append(rows, flatButton("Reload saved version…").OnClick(func() { d.openNoteDocument(u.document.ID) }))
	}
	return memoryKeyboardMount(d, pageScroll(ui.VBox(rows...).Spacing(14).CrossAlign(layout.CrossStretch).ID("memory-composer-root")))
}
