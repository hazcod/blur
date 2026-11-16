package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
)

var errMarkdownArchiveCancelled = errors.New("markdown archive export canceled")

func (d *desktop) exportSavedMarkdownNotes() {
	if d == nil || d.client == nil || d.memoryUI.notesExporting || d.memoryUI.loading || d.client.data == nil {
		return
	}
	choose := d.memoryUI.chooseNotesExport
	if choose == nil {
		choose = chooseMarkdownArchive
	}
	path, err := choose("Blur Notes.zip")
	if errors.Is(err, errMarkdownArchiveCancelled) {
		return
	}
	if err != nil {
		d.memoryUI.status = "The notes archive could not be created. Choose another location and try again."
		d.app.RequestUpdate()
		return
	}
	if filepath.Ext(path) == "" {
		path += ".zip"
	}
	epoch, stable := d.client.BeginMemoryRead()
	if !stable {
		d.memoryUI.status = "Saved notes are changing. Try exporting again in a moment."
		d.app.RequestUpdate()
		return
	}
	u := &d.memoryUI
	if u.notesExportCancel != nil {
		u.notesExportCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	u.notesExportCancel = cancel
	u.notesExportRequest++
	u.notesExporting, u.status = true, ""
	request, generation := u.notesExportRequest, d.memoryGeneration
	d.app.RequestUpdate()
	go func() {
		defer cancel()
		stage, count, exportErr := stageMarkdownNotesArchive(path, func(output io.Writer) (int, error) {
			pages, ok := d.client.data.(noteDocumentPageReader)
			if !ok {
				return 0, ErrNotConfigured
			}
			return writeMarkdownNotesArchive(ctx, output, pages)
		})
		if exportErr == nil && ctx.Err() != nil {
			exportErr = ctx.Err()
		}
		if exportErr == nil {
			if count == 0 {
				stage.discard()
				exportErr = errNoSavedNotes
			} else if !d.client.PublishMemoryRead(epoch, func() { exportErr = stage.commit() }) {
				exportErr = ErrMemoryChanging
			}
		}
		if stage != nil {
			stage.discard()
		}
		d.app.Post(func() {
			if request != d.memoryUI.notesExportRequest || generation != d.memoryGeneration {
				return
			}
			d.memoryUI.notesExportCancel = nil
			d.memoryUI.notesExporting = false
			switch {
			case errors.Is(exportErr, context.Canceled):
				d.memoryUI.status = "Export canceled. No archive was saved."
			case errors.Is(exportErr, errNoSavedNotes):
				d.memoryUI.status = "There are no saved notes to export yet."
			case errors.Is(exportErr, errNotesArchiveCatchingUp):
				d.memoryUI.status = "Some older notes are still being prepared. Try exporting again when they’re ready."
			case errors.Is(exportErr, ErrMemoryChanging):
				d.memoryUI.status = "Saved notes changed during export. No archive was saved; try again."
			case exportErr != nil:
				d.memoryUI.status = "The notes archive could not be created. Your saved notes are unchanged. Try another location."
			case count == 1:
				d.memoryUI.status = "Exported 1 note as Markdown."
			default:
				d.memoryUI.status = fmt.Sprintf("Exported %d notes as Markdown.", count)
			}
			d.app.RequestUpdate()
		})
	}()
}
