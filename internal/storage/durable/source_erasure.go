package durable

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"github.com/hazcod/blur/internal/tasks"
)

const erasureNamespace = "source-erase-v1"
const erasureJournalID = "pending"

type sourceErasure struct {
	SourceID    string `json:"source_id"`
	RequestedID string `json:"requested_id,omitempty"`
	Phase       int    `json:"phase"`
	After       string `json:"after,omitempty"`
	Part        int64  `json:"part,omitempty"`
	PartPhase   int    `json:"part_phase,omitempty"`
	PartAfter   string `json:"part_after,omitempty"`
	VisitPage   int64  `json:"visit_page,omitempty"`
	VisitRow    int64  `json:"visit_row,omitempty"`
	StageAfter  string `json:"stage_after,omitempty"`
	// Preserve the original journal field names for restart compatibility.
	CaptureScope bool   `json:"document_scope,omitempty"`
	Family       string `json:"family,omitempty"`
	Cutoff       int64  `json:"cutoff,omitempty"`
	HasCutoff    bool   `json:"has_cutoff,omitempty"`
	Inclusive    bool   `json:"inclusive,omitempty"`
	CaptureAfter string `json:"document_after,omitempty"`
	Removed      int64  `json:"removed,omitempty"`
	MeetingID    string `json:"meeting_id,omitempty"`
	MeetingLast  uint64 `json:"meeting_last,omitempty"`
	MeetingNext  uint64 `json:"meeting_next,omitempty"`
	NoteID       string `json:"note_id,omitempty"`
	NoteLast     uint64 `json:"note_last,omitempty"`
	NoteNext     uint64 `json:"note_next,omitempty"`
	NoteAt       int64  `json:"note_at,omitempty"`
}

// Every step and its cursor commit together. No intermediate projection is
// published: the store gate rejects reads/writes and the exclusive lock drains
// transactions that entered before the gate. A failed run retains the gate;
// retry or reopen resumes the authenticated journal before serving any data.
func (s *Store) deleteSourceStaged(ctx context.Context, sourceID string, afterStep func() error) error {
	_, err := s.deleteSourceStagedIf(ctx, sourceID, afterStep, nil)
	return err
}

// DeleteObservationRecordBefore checks the immutable observation capture time,
// or the latest real capture visit when the source is a capture edition,
// while holding the erasure gate. Preparation receives the exact task snapshot
// under that gate and must not call back into storage. The caller pauses native
// scheduling before entering here, preserving the service->store lock order.
func (s *Store) DeleteObservationRecordBefore(ctx context.Context, sourceID string, cutoff time.Time, inclusive bool, prepare func([]tasks.Item) error) (bool, error) {
	var scoped func(string, []tasks.Item) error
	if prepare != nil {
		scoped = func(_ string, items []tasks.Item) error { return prepare(items) }
	}
	return s.DeleteSourceRecordBeforeWithPrepare(ctx, sourceID, cutoff, inclusive, scoped)
}

// DeleteSourceRecordBeforeWithPrepare retains cutoff authority while preparing
// native notifications and live activity for the whole authenticated source.
// Callback invocations are bounded per part and must not reenter storage.
func (s *Store) DeleteSourceRecordBeforeWithPrepare(ctx context.Context, sourceID string, cutoff time.Time, inclusive bool, prepare func(string, []tasks.Item) error) (bool, error) {
	if !validCaptureTime(cutoff) {
		return false, &storage.StoreError{Code: storage.Invalid}
	}
	return s.deleteSourceStagedIf(ctx, sourceID, nil, func(reader bolt.Reader) (bool, error) {
		resolvedID, _, err := s.resolveCaptureSourceReader(reader, sourceID)
		if err != nil {
			return false, err
		}
		sourceID := resolvedID
		if edition, _, found, err := s.captureEditionReader(reader, sourceID); err != nil {
			return false, err
		} else if found {
			at := time.Unix(0, edition.LastSeen).UTC()
			if at.After(cutoff) || !inclusive && at.Equal(cutoff) {
				return false, nil
			}
		}
		key := s.logicalKey(groupRecord, kindObservation, "", sourceID)
		defer clear(key)
		live, err := reader.Has(key)
		if err != nil {
			return false, mapStorePhysicalError(err)
		}
		if live {
			at, err := s.observationCapturedAtReader(reader, sourceID)
			if err != nil {
				return false, err
			}
			if at.IsZero() || at.After(cutoff) || !inclusive && at.Equal(cutoff) {
				return false, nil
			}
		}
		// After checking captures, a missing primary cannot be a newer capture.
		// Still cascade through
		// authenticated legacy derivatives rather than leaving orphaned text.
		if err := s.prepareSourceErasureReader(ctx, reader, sourceID, prepare); err != nil {
			return false, err
		}
		return true, nil
	})
}

func (s *Store) deleteSourceStagedIf(ctx context.Context, sourceID string, afterStep func() error, admit func(bolt.Reader) (bool, error)) (bool, error) {
	if ctx == nil || !validHash(sourceID) {
		return false, &storage.StoreError{Code: storage.Invalid}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if s == nil || s.state == nil {
		return false, &storage.StoreError{Code: storage.Closed}
	}
	state := s.state
	state.deletionPending.Store(true)
	state.mu.Lock()
	defer state.mu.Unlock()
	// Another erasure may have cleared the gate while this writer waited.
	state.deletionPending.Store(true)
	if state.closed || state.db == nil {
		return false, &storage.StoreError{Code: storage.Closed}
	}
	journal, found, err := s.readSourceErasureLocked(ctx)
	// A failed journal read cannot establish that partial erasure is absent.
	if err != nil {
		return false, err
	}
	durable := found
	defer func() {
		if !durable {
			state.deletionPending.Store(false)
		}
	}()
	requestedID := sourceID
	if found {
		if journal.SourceID != sourceID && journal.RequestedID != sourceID {
			return false, ErrDeletionPending
		}
	} else {
		var resolveErr error
		if err := state.db.View(ctx, func(reader bolt.Reader) error {
			sourceID, _, resolveErr = s.resolveCaptureSourceReader(reader, sourceID)
			return resolveErr
		}); err != nil {
			if resolveErr != nil {
				return false, resolveErr
			}
			return false, mapStorePhysicalError(err)
		}
	}
	if !found {
		if admit != nil {
			allowed := false
			var callbackErr error
			err := state.db.View(ctx, func(reader bolt.Reader) error {
				allowed, callbackErr = admit(reader)
				if callbackErr != nil {
					return errAbortTransaction
				}
				return nil
			})
			if callbackErr != nil {
				return false, callbackErr
			}
			if err != nil {
				return false, mapStorePhysicalError(err)
			}
			if !allowed {
				return false, nil
			}
			// Most conditional deletions (including absent-source replays) fit
			// one transaction. Keep the gate and preparation decision, and use
			// resumable stages only after a classified atomic budget failure.
			err = s.updateLocked(ctx, func(tx *Tx) error { return tx.deleteObservationRecord(sourceID) })
			if err == nil {
				return true, nil
			}
			if !errors.Is(err, ErrTransactionBudget) && !errors.Is(err, ErrDeletionPending) {
				return false, err
			}
		}
		journal = sourceErasure{SourceID: sourceID}
		if requestedID != sourceID {
			journal.RequestedID = requestedID
		}
		if err := s.updateLocked(ctx, func(tx *Tx) error { return tx.PutIndexRecord(erasureNamespace, erasureJournalID, mustJSON(journal)) }); err != nil {
			return false, err
		}
		durable = true
	}
	return s.runSourceErasureLocked(ctx, &journal, afterStep)
}

func (s *Store) runSourceErasureLocked(ctx context.Context, journal *sourceErasure, afterStep func() error) (bool, error) {
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		done := false
		next := *journal
		err := s.updateLocked(ctx, func(tx *Tx) error {
			finished, err := tx.eraseSourceStep(&next)
			if err != nil {
				return err
			}
			if finished {
				done = true
				return tx.deleteKnowledgeRecord(erasureNamespace, erasureJournalID)
			}
			return tx.PutIndexRecord(erasureNamespace, erasureJournalID, mustJSON(next))
		})
		if err != nil {
			return false, err
		}
		*journal = next
		if done {
			s.state.deletionPending.Store(false)
			return true, nil
		}
		if afterStep != nil {
			if err := afterStep(); err != nil {
				return false, err
			}
		}
	}
}

func (s *Store) readSourceErasureLocked(ctx context.Context) (sourceErasure, bool, error) {
	var journal sourceErasure
	var found bool
	var readErr error
	err := s.state.db.View(ctx, func(reader bolt.Reader) error {
		var encoded []byte
		encoded, found, readErr = s.readIndexValueReader(reader, erasureNamespace, erasureJournalID, 2048)
		defer clear(encoded)
		if readErr != nil {
			return errAbortTransaction
		}
		if found && (decodeCanonicalJSON(encoded, &journal) != nil || !journal.valid()) {
			readErr = ErrCorrupt
			return errAbortTransaction
		}
		return nil
	})
	if readErr != nil {
		return sourceErasure{}, false, readErr
	}
	return journal, found, mapStorePhysicalError(err)
}

func (s *Store) resumePendingSourceDeletion(ctx context.Context) error {
	return s.RecoverSourceDeletion(ctx)
}

// RecoverSourceDeletion finishes an already committed erasure intent. The
// coordinator calls it before a retry reads task state for native cleanup;
// that cleanup was confirmed before the original intent was committed.
func (s *Store) RecoverSourceDeletion(ctx context.Context) error {
	if ctx == nil {
		return &storage.StoreError{Code: storage.Invalid}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.state == nil {
		return &storage.StoreError{Code: storage.Closed}
	}
	if !s.state.mu.TryRLock() {
		return ErrDeletionPending
	}
	if s.state.closed || s.state.db == nil {
		s.state.mu.RUnlock()
		return &storage.StoreError{Code: storage.Closed}
	}
	journal, found, err := s.readSourceErasureLocked(ctx)
	if err == nil && !found {
		s.state.deletionPending.Store(false)
	}
	s.state.mu.RUnlock()
	if err != nil || !found {
		return err
	}
	return s.deleteSourceStaged(ctx, journal.SourceID, nil)
}

func (tx *Tx) eraseSourceStep(journal *sourceErasure) (bool, error) {
	var complete bool
	var err error
	sourceID := journal.SourceID
	switch journal.Phase {
	case 0:
		tx.mu.Lock()
		ids, readErr := tx.store.taskSourceReader(tx.writer, sourceID)
		err = readErr
		if err == nil {
			complete = len(ids) == 0
			if !complete {
				item, found, readErr := tx.store.taskReader(tx.writer, ids[0])
				err = readErr
				if err == nil && (!found || !item.References(sourceID)) {
					err = ErrCorrupt
				}
				if err == nil {
					err = tx.deleteTaskLocked(item)
				}
			}
		}
		tx.mu.Unlock()
	case 1:
		complete, err = tx.eraseOneInferredSource(sourceID)
	case 2:
		var ids []string
		ids, err = tx.knowledgeDirectory()
		if err == nil {
			n := sort.Search(len(ids), func(n int) bool { return ids[n] > journal.After })
			complete = n == len(ids)
			if !complete {
				entity, found, readErr := tx.knowledgeEntity(ids[n])
				err = readErr
				if err == nil && found {
					err = tx.quarantineKnowledgeEntitySource(entity, sourceID)
				}
				journal.After = ids[n]
			}
		}
	case 3:
		complete, err = tx.removeTodaySourceBatch(sourceID, 1)
	case 4:
		complete, err = tx.removeActivitySourceBatch(sourceID, 1)
	case 5:
		complete, err = tx.removeChroniclesForSourceBatch(sourceID)
	case 6:
		err = tx.removeSemanticSource(sourceID)
		complete = true
	case 7:
		tx.mu.Lock()
		unlinkErr := tx.removeMeetingSourceLocked(sourceID)
		tx.mu.Unlock()
		if unlinkErr != nil {
			return false, unlinkErr
		}
		drained, err := tx.eraseObservationSighting(sourceID)
		if err != nil || !drained {
			return false, err
		}
		_, found, readErr := tx.store.getObservationReader(tx.writer, sourceID)
		if readErr != nil {
			return false, readErr
		}
		if found {
			tx.mu.Lock()
			drained, drainErr := tx.removePagedSearchBatchLocked(sourceID, true)
			tx.mu.Unlock()
			if drainErr != nil || !drained {
				return false, drainErr
			}
			err = tx.deleteObservation(sourceID)
		}
		complete = err == nil
	case 8:
		complete, err = tx.captureErasePartsStep(journal)
	case 9:
		complete, err = tx.captureEraseVisitsStep(journal)
	case 10:
		complete, err = tx.captureEraseDuplicatesStep(journal)
	case 11:
		err = tx.captureUnlinkEdition(sourceID)
		if err == nil && journal.CaptureScope {
			if journal.Removed == int64(^uint(0)>>1) {
				return false, ErrCorrupt
			}
			journal.Removed++
			journal.Phase = 12
			journal.SourceID = journal.capturePlanID()
			return false, nil
		}
		if err == nil && journal.MeetingID != "" {
			journal.MeetingNext++
			journal.Phase = 14
			journal.SourceID = tx.store.meetingDigest("erase", []byte(journal.MeetingID))
			return false, nil
		}
		if err == nil && journal.NoteID != "" {
			journal.NoteNext++
			journal.Phase = 15
			journal.SourceID = journal.NoteID
			return false, nil
		}
		return err == nil, err
	case 12:
		return false, tx.capturePlanErasureStep(journal)
	case 13:
		return tx.captureErasePendingStep(journal)
	case 14:
		return tx.meetingPlanErasureStep(journal)
	case 15:
		return tx.noteDocumentPlanErasureStep(journal)
	default:
		return false, ErrCorrupt
	}
	if err == nil && complete {
		journal.Phase++
		journal.VisitRow = 0
		journal.After = ""
		journal.Part = 0
		journal.PartPhase = 0
		journal.PartAfter = ""
		journal.VisitPage = 0
		journal.StageAfter = ""
	}
	return false, err
}

func (tx *Tx) eraseOneInferredSource(sourceID string) (bool, error) {
	tx.mu.Lock()
	progress, found, err := tx.store.readEnrichmentProgress(tx.writer, sourceID)
	if err != nil {
		tx.mu.Unlock()
		return false, err
	}
	if !found || len(progress.Entities) == 0 {
		tx.mu.Unlock()
		return true, tx.removeEnrichmentSource(sourceID)
	}
	one := progress
	one.Entities = progress.Entities[:1]
	if err := tx.removeInferredClaims(one); err != nil {
		tx.mu.Unlock()
		return false, err
	}
	progress.Entities = progress.Entities[1:]
	err = tx.putEnrichmentProgress(progress)
	tx.mu.Unlock()
	return false, err
}
