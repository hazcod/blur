package durable

import (
	"context"
	"encoding/json"

	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

// Admission is resumable and bounded by 32 sources / two MiB per call. New
// notes already maintain the directory transactionally; opening the store never
// scans historical plaintext to enable editing or browsing.
func (s *Store) migrateNoteDocumentsPage(ctx context.Context) (bool, error) {
	s.state.noteMu.Lock()
	defer s.state.noteMu.Unlock()
	var migration noteDocumentMigration
	encoded, found, err := s.readIndexValue(ctx, noteDocumentMetaNS, "migration", 160)
	if err != nil {
		return false, err
	}
	if found && (json.Unmarshal(encoded, &migration) != nil || migration.After != "" && !validHash(migration.After)) {
		clear(encoded)
		return false, ErrCorrupt
	}
	clear(encoded)
	if migration.Done {
		return false, nil
	}
	page, err := s.ListObservationPage(ctx, storage.ObservationPageRequest{After: migration.After, Limit: 32, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil {
		return false, err
	}
	err = s.noteDocumentUpdate(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		for i, o := range page.Observations {
			// The primary may have been erased after the read snapshot.
			key := s.logicalKey(groupRecord, kindObservation, "", o.ContentID())
			live, err := tx.writer.Has(key)
			clear(key)
			if err != nil {
				return mapStorePhysicalError(err)
			}
			if live {
				if err := tx.admitLegacyNoteLocked(o, page.CapturedAt[i]); err != nil {
					return err
				}
			}
			migration.After = o.ContentID()
		}
		migration.Done = !page.HasMore
		return tx.putIndexValueLocked(noteDocumentMetaNS, "migration", mustJSON(migration))
	})
	return !migration.Done, err
}

// ListNoteDocumentsPage reads only encrypted date directories, current heads,
// and the requested source bodies. Prior revisions are never hydrated here.
func (s *Store) ListNoteDocumentsPage(ctx context.Context, request notes.DocumentPageRequest) (notes.DocumentPage, error) {
	if ctx == nil || s == nil || s.state == nil || request.Limit < 1 || request.Limit > storage.MaxObservationPageRows || request.MaxBytes < 1 || request.MaxBytes > storage.MaxObservationPageBytes || request.BeforeUpdatedAt.IsZero() != (request.BeforeID == "") || request.BeforeID != "" && !validHash(request.BeforeID) {
		return notes.DocumentPage{}, notes.ErrInvalid
	}
	catchingUp, err := s.migrateNoteDocumentsPage(ctx)
	if err != nil {
		return notes.DocumentPage{}, err
	}
	page := notes.DocumentPage{Documents: make([]notes.Document, 0, request.Limit), CatchingUp: catchingUp}
	err = s.viewWithReadLimit(ctx, bolt.MaxReadTransactionOps, func(r bolt.Reader) error {
		days, err := s.noteDocumentDaysReader(r)
		if err != nil {
			return err
		}
		used := 0
		for i := len(days) - 1; i >= 0; i-- {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !request.BeforeUpdatedAt.IsZero() && days[i] > request.BeforeUpdatedAt.UTC().Format("2006-01-02") {
				continue
			}
			rows, err := s.noteDocumentDayReader(r, days[i])
			if err != nil {
				return err
			}
			for _, row := range rows {
				if !request.BeforeUpdatedAt.IsZero() && !noteOrderBefore(noteDocumentOrder{request.BeforeID, request.BeforeUpdatedAt}, row) {
					continue
				}
				if len(page.Documents) >= request.Limit {
					page.HasMore = true
					return nil
				}
				d, found, err := s.noteDocumentReader(r, row.ID)
				if err != nil {
					return err
				}
				if !found || !d.UpdatedAt.Equal(row.At) {
					return ErrCorrupt
				}
				size, err := s.observationSizeReader(r, d.SourceID)
				if err != nil {
					return err
				}
				if size > request.MaxBytes-used {
					page.HasMore = true
					return nil
				}
				o, live, err := s.getObservationReader(r, d.SourceID)
				if err != nil {
					return err
				}
				if !live {
					return ErrCorrupt
				}
				d.Text = o.Text()
				used += size
				page.Documents = append(page.Documents, d)
				page.NextUpdatedAt = d.UpdatedAt
				page.NextID = d.ID
			}
		}
		return nil
	})
	if err != nil {
		return notes.DocumentPage{}, err
	}
	return page, nil
}

func (s *Store) observationSizeReader(r bolt.Reader, id string) (int, error) {
	key := s.logicalKey(groupOrder, kindObservation, "", id)
	defer clear(key)
	value, found, err := r.Get(key)
	defer clear(value)
	if err != nil {
		return 0, mapStorePhysicalError(err)
	}
	if !found {
		return 0, ErrCorrupt
	}
	size, _, recordKey, err := s.decodeSizeEntry(key, value)
	clear(recordKey)
	return size, err
}
