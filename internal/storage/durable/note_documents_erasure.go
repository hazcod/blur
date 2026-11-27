package durable

import (
	"context"
	"time"

	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"github.com/hazcod/blur/internal/tasks"
)

// DeleteNoteDocument removes every immutable revision through the existing
// resumable erasure journal. The prepare variant coordinates native reminders
// before the durable intent commits; callbacks must not reenter the store.
func (s *Store) DeleteNoteDocument(ctx context.Context, id string, expected uint64) error {
	return s.deleteNoteDocument(ctx, id, expected, nil, nil)
}
func (s *Store) DeleteNoteDocumentWithPrepare(ctx context.Context, id string, expected uint64, prepare func(string, []tasks.Item) error) error {
	return s.deleteNoteDocument(ctx, id, expected, prepare, nil)
}
func (s *Store) deleteNoteDocument(ctx context.Context, id string, expected uint64, prepare func(string, []tasks.Item) error, afterStep func() error) error {
	if ctx == nil || !validHash(id) || expected == 0 || expected >= 1<<48 {
		return notes.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.state == nil {
		return &storage.StoreError{Code: storage.Closed}
	}
	state := s.state
	state.deletionPending.Store(true)
	state.mu.Lock()
	defer state.mu.Unlock()
	state.deletionPending.Store(true)
	if state.closed || state.db == nil {
		return &storage.StoreError{Code: storage.Closed}
	}
	journal, pending, err := s.readSourceErasureLocked(ctx)
	if err != nil {
		return err
	}
	durable := pending
	defer func() {
		if !durable {
			state.deletionPending.Store(false)
		}
	}()
	if pending && journal.NoteID != id {
		return ErrDeletionPending
	}
	if !pending {
		var d notes.Document
		var found bool
		var fenced bool
		var readErr error
		err = state.db.View(ctx, func(r bolt.Reader) error {
			d, found, readErr = s.noteDocumentReader(r, id)
			if readErr == nil && !found {
				key := s.logicalKey(groupRecord, kindIndex, "index:"+noteDocumentFenceNS, id)
				defer clear(key)
				fenced, readErr = r.Has(key)
			}
			return readErr
		})
		if readErr != nil {
			return readErr
		}
		if err != nil {
			return mapStorePhysicalError(err)
		}
		if !found {
			if fenced {
				return nil
			}
			return notes.ErrDocumentNotFound
		}
		if d.Revision != expected {
			return notes.ErrDocumentConflict
		}
		// Bounded read transactions avoid cloning an entire revision history.
		if prepare != nil {
			for revision := uint64(1); revision <= d.Revision; revision++ {
				readErr = nil
				var sourceID string
				err = state.db.View(ctx, func(r bolt.Reader) error {
					b, live, e := s.readIndexValueReader(r, noteDocumentRevisionNS, noteRevisionKey(id, revision), 64)
					defer clear(b)
					if e != nil {
						readErr = e
						return e
					}
					if !live {
						return nil
					}
					sourceID = string(b)
					if !validHash(sourceID) {
						readErr = ErrCorrupt
						return readErr
					}
					return nil
				})
				if readErr != nil {
					return readErr
				}
				if err != nil {
					return mapStorePhysicalError(err)
				}
				if sourceID != "" {
					if err := s.prepareSourceErasureLocked(ctx, sourceID, prepare); err != nil {
						return err
					}
				}
			}
		}
		at := time.Now()
		if at.Before(d.UpdatedAt) {
			at = d.UpdatedAt
		}
		journal = sourceErasure{SourceID: id, NoteID: id, NoteLast: d.Revision, NoteNext: 1, NoteAt: at.UnixNano(), Phase: 15}
		err = s.updateLocked(ctx, func(tx *Tx) error {
			tx.mu.Lock()
			defer tx.mu.Unlock()
			if err := tx.removeNoteDocumentHeadLocked(d); err != nil {
				return err
			}
			return tx.putIndexValueLocked(erasureNamespace, erasureJournalID, mustJSON(journal))
		})
		if err != nil {
			return err
		}
		durable = true
	}
	_, err = s.runSourceErasureLocked(ctx, &journal, afterStep)
	return err
}

func (tx *Tx) removeNoteDocumentHeadLocked(d notes.Document) error {
	if err := tx.changeNoteDocumentOrderLocked(d, false); err != nil {
		return err
	}
	if err := tx.putIndexValueLocked(noteDocumentFenceNS, d.ID, []byte{1}); err != nil {
		return err
	}
	return tx.deleteIndexValueLocked(noteDocumentNS, d.ID)
}

// Individual retention/source erasure also removes its exact metadata edge.
// It never deletes a newer head just because an older revision has expired.
func (tx *Tx) removeNoteDocumentSourceLocked(sourceID string) error {
	ref, found, err := tx.store.noteDocumentSourceReader(tx.writer, sourceID)
	if err != nil || !found {
		return err
	}
	d, live, err := tx.store.noteDocumentReader(tx.writer, ref.ID)
	if err != nil {
		return err
	}
	if live && d.SourceID == sourceID {
		if err := tx.removeNoteDocumentHeadLocked(d); err != nil {
			return err
		}
	}
	if err := tx.deleteIndexValueLocked(noteDocumentRevisionNS, noteRevisionKey(ref.ID, ref.Revision)); err != nil {
		return err
	}
	return tx.deleteIndexValueLocked(noteDocumentSourceNS, sourceID)
}

func (tx *Tx) noteDocumentPlanErasureStep(j *sourceErasure) (bool, error) {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if j.NoteID == "" || j.NoteNext == 0 {
		return false, ErrCorrupt
	}
	if j.NoteNext > j.NoteLast {
		return true, nil
	}
	b, found, err := tx.readIndexValueLocked(noteDocumentRevisionNS, noteRevisionKey(j.NoteID, j.NoteNext), 64)
	defer clear(b)
	if err != nil {
		return false, err
	}
	if !found {
		j.NoteNext++
		return false, nil
	}
	if !validHash(string(b)) {
		return false, ErrCorrupt
	}
	j.SourceID = string(b)
	if err := tx.putDeletionCutoffLocked(Tombstone{SourceID: j.SourceID, At: time.Unix(0, j.NoteAt).UTC()}); err != nil {
		return false, err
	}
	j.Phase = 0
	return false, nil
}
