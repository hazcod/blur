package client

import (
	"context"
	"errors"
	"time"

	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/storage/durable"
)

type noteDocumentReader interface {
	GetNoteDocument(context.Context, string) (notes.Document, bool, error)
}
type noteDocumentWriter interface {
	UpdateNoteDocument(context.Context, string, uint64, string, time.Time) (notes.Document, error)
}
type noteDocumentCreator interface {
	CreateNoteDocument(context.Context, string, time.Time) (notes.Document, error)
}
type noteDocumentDeleter interface {
	DeleteNoteDocument(context.Context, string, uint64) error
}

func (c *Client) NoteDocument(ctx context.Context, id string) (notes.Document, error) {
	if c == nil || ctx == nil {
		return notes.Document{}, ErrNotConfigured
	}
	reader, ok := c.data.(noteDocumentReader)
	if !ok {
		return notes.Document{}, ErrNotConfigured
	}
	ctx, done, err := c.beginOperation(ctx, shortOperationTimeout)
	if err != nil {
		return notes.Document{}, err
	}
	defer done()
	d, found, err := reader.GetNoteDocument(ctx, id)
	if err != nil {
		return notes.Document{}, err
	}
	if !found {
		return notes.Document{}, notes.ErrDocumentNotFound
	}
	return d, nil
}

func (c *Client) CreateNoteDocument(ctx context.Context, text string) (notes.Document, error) {
	if c == nil || ctx == nil {
		return notes.Document{}, ErrNotConfigured
	}
	creator, ok := c.data.(noteDocumentCreator)
	if !ok {
		return notes.Document{}, ErrNotConfigured
	}
	ctx, done, err := c.beginOperation(ctx, noteOperationTimeout)
	if err != nil {
		return notes.Document{}, err
	}
	defer done()
	epoch, stable := c.BeginMemoryRead()
	if !stable {
		return notes.Document{}, ErrMemoryChanging
	}
	ctx, cancel := context.WithCancel(ctx)
	id, stable := c.registerAskEpoch(cancel, &epoch)
	if !stable {
		cancel()
		return notes.Document{}, ErrMemoryChanging
	}
	defer func() { cancel(); c.unregisterAsk(id) }()
	d, err := creator.CreateNoteDocument(ctx, text, time.Now())
	if err != nil {
		return notes.Document{}, err
	}
	c.memoryIndex.NotifySource(d.SourceID)
	c.localAI.NotifyLibrarySource(d.SourceID)
	result := notes.Result{SourceID: d.SourceID, CreatedAt: d.UpdatedAt, Processing: notes.ProcessingDeterministic}
	if service, ok := c.notes.(*notes.Service); ok {
		if receipt, e := service.ProcessSavedNoteReceipt(ctx, d.SourceID); e == nil {
			result = receipt
		}
	}
	c.queueNoteProcessing(ctx, result, epoch)
	return d, nil
}

// Failed compare-and-save leaves the editor's draft and memory epoch intact.
// Successful saves retire active reads, then schedule derivation of only the new
// revision. The caller receives the committed document despite epoch advance.
func (c *Client) UpdateNoteDocument(ctx context.Context, id string, expected uint64, text string) (notes.Document, error) {
	if c == nil || ctx == nil {
		return notes.Document{}, ErrNotConfigured
	}
	writer, ok := c.data.(noteDocumentWriter)
	if !ok {
		return notes.Document{}, ErrNotConfigured
	}
	ctx, done, err := c.beginOperation(ctx, noteOperationTimeout)
	if err != nil {
		return notes.Document{}, err
	}
	defer done()
	previous, err := c.NoteDocument(ctx, id)
	if err != nil {
		return notes.Document{}, err
	}
	at := time.Now()
	if at.Before(previous.UpdatedAt) {
		at = previous.UpdatedAt
	}
	// Serialize the commit with deletion admission and derived publication. This
	// lock also prevents an old Ask result publishing between commit and epoch
	// retirement; errors do not trigger the user-visible invalidation path.
	c.memoryStartMu.Lock()
	c.memoryChangeMu.Lock()
	if c.memoryMutationDepth != 0 {
		c.memoryChangeMu.Unlock()
		c.memoryStartMu.Unlock()
		return notes.Document{}, ErrMemoryChanging
	}
	d, err := writer.UpdateNoteDocument(ctx, id, expected, text, at)
	if err == nil {
		c.memoryMutationEpoch++
		if c.memoryMutationEpoch == 0 {
			c.memoryMutationEpoch++
		}
	}
	c.memoryChangeMu.Unlock()
	c.memoryStartMu.Unlock()
	if err != nil {
		return notes.Document{}, err
	}
	c.NotifyMemoryChanging()
	c.NotifyMemoryStable()
	c.memoryIndex.NotifySource(d.SourceID)
	c.localAI.NotifyLibrarySource(d.SourceID)
	result := notes.Result{SourceID: d.SourceID, CreatedAt: d.UpdatedAt, Processing: notes.ProcessingDeterministic}
	if service, ok := c.notes.(*notes.Service); ok {
		if receipt, e := service.ProcessSavedNoteReceipt(ctx, d.SourceID); e == nil {
			result = receipt
		}
	}
	epoch, stable := c.BeginMemoryRead()
	if stable {
		c.queueNoteProcessing(ctx, result, epoch)
	}
	return d, nil
}

func (c *Client) DeleteNoteDocument(ctx context.Context, id string, expected uint64) error {
	if c == nil || ctx == nil {
		return ErrNotConfigured
	}
	deleter, ok := c.deletion.(noteDocumentDeleter)
	if !ok {
		return ErrNotConfigured
	}
	ctx, done, err := c.beginOperation(ctx, noteOperationTimeout)
	if err != nil {
		return err
	}
	defer done()
	// Reject a stale editor before invalidating its visible draft.
	d, err := c.NoteDocument(ctx, id)
	if err != nil && !errors.Is(err, durable.ErrDeletionPending) && !errors.Is(err, notes.ErrDocumentNotFound) {
		return err
	}
	if err == nil && d.Revision != expected {
		return notes.ErrDocumentConflict
	}
	if d.ID == "" {
		d.ID = id
	}
	c.NotifyMemoryChanging()
	defer c.NotifyMemoryStable()
	return deleter.DeleteNoteDocument(ctx, d.ID, expected)
}
