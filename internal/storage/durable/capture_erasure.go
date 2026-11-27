package durable

import (
	"context"
	"time"

	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"github.com/hazcod/blur/internal/tasks"
)

const captureErasurePlanID = "e12d1b411a647eaf8551681087c2a34ecb967f357c550697eb366f781b7688281"

func (j sourceErasure) capturePlanID() string {
	if j.Family != "" {
		return j.Family
	}
	return captureErasurePlanID
}

func (j sourceErasure) valid() bool {
	if !validHash(j.SourceID) || j.RequestedID != "" && (!validHash(j.RequestedID) || j.CaptureScope || j.MeetingID != "" || j.NoteID != "") || j.Phase < 0 || j.Phase > 15 ||
		j.After != "" && (j.Phase != 2 || !validOpaqueID(j.After)) ||
		j.Part < 0 || j.Part != 0 && j.Phase != 8 ||
		j.PartPhase < 0 || j.PartPhase > 8 || j.PartPhase != 0 && j.Phase != 8 ||
		j.PartAfter != "" && (j.Phase != 8 || j.PartPhase != 2 || !validOpaqueID(j.PartAfter)) ||
		j.RangeID != "" && (j.Phase != 8 || !validHash(j.RangeID)) || j.RangePhase < 0 || j.RangePhase > 8 || j.RangePhase != 0 && j.RangeID == "" || j.RangeAfter != "" && (j.RangePhase != 2 || !validOpaqueID(j.RangeAfter)) ||
		j.VisitPage < 0 || j.VisitPage != 0 && j.Phase != 9 ||
		j.VisitRow < 0 || j.VisitRow >= captureVisitRows || j.VisitRow != 0 && j.Phase != 9 ||
		j.StageAfter != "" && (j.Phase != 10 && j.Phase != 13 || !validHash(j.StageAfter)) {
		return false
	}
	if j.NoteID != "" {
		return validHash(j.NoteID) && validCaptureTime(time.Unix(0, j.NoteAt)) && j.MeetingID == "" && j.MeetingLast == 0 && j.MeetingNext == 0 && !j.CaptureScope && j.Family == "" && j.Cutoff == 0 && !j.HasCutoff && !j.Inclusive && j.CaptureAfter == "" && j.Removed == 0 && j.NoteLast > 0 && j.NoteLast < 1<<48 && j.NoteNext > 0 && j.NoteNext <= j.NoteLast+1 && (j.Phase <= 11 || j.Phase == 15)
	}
	if j.NoteLast != 0 || j.NoteNext != 0 || j.NoteAt != 0 {
		return false
	}
	if j.MeetingID != "" {
		return meetingID(j.MeetingID, 64) && !j.CaptureScope && j.Family == "" && j.Cutoff == 0 && !j.HasCutoff && !j.Inclusive && j.CaptureAfter == "" && j.Removed == 0 && j.MeetingLast < 1<<48 && j.MeetingNext > 0 && j.MeetingNext <= j.MeetingLast+1 && (j.Phase <= 11 || j.Phase == 14)
	}
	if j.MeetingLast != 0 || j.MeetingNext != 0 {
		return false
	}
	if !j.CaptureScope {
		return j.Phase <= 11 && j.Family == "" && j.Cutoff == 0 && !j.HasCutoff && !j.Inclusive && j.CaptureAfter == "" && j.Removed == 0
	}
	return captureOptionalID(j.Family) && captureOptionalID(j.CaptureAfter) &&
		(j.HasCutoff && validCaptureTime(time.Unix(0, j.Cutoff).UTC()) || !j.HasCutoff && j.Cutoff == 0) &&
		(j.HasCutoff || !j.Inclusive) && (j.Family == "" || j.HasCutoff && j.Inclusive) &&
		j.Removed >= 0 && j.Removed <= int64(^uint(0)>>1) &&
		(j.Phase < 12 || j.SourceID == j.capturePlanID()) && (j.Phase != 13 || j.CaptureAfter == "")
}

func (j sourceErasure) selects(at int64) bool {
	return !j.HasCutoff || at < j.Cutoff || j.Inclusive && at == j.Cutoff
}

// The caller already holds the same native scheduling guard used by conditional
// source deletion. All native requests are confirmed before intent commit;
// recovery then needs only the existing encrypted-store erasure gate.
func (s *Store) deleteCapturesWithHook(ctx context.Context, familyID string, cutoff *time.Time, inclusive bool, prepare func(string, []tasks.Item) error, afterStep func() error) (int, error) {
	if ctx == nil || !captureOptionalID(familyID) || cutoff != nil && !validCaptureTime(*cutoff) || familyID != "" && (cutoff == nil || !inclusive) {
		return 0, &storage.StoreError{Code: storage.Invalid}
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if s == nil || s.state == nil {
		return 0, &storage.StoreError{Code: storage.Closed}
	}
	state := s.state
	state.deletionPending.Store(true)
	state.mu.Lock()
	defer state.mu.Unlock()
	state.deletionPending.Store(true)
	if state.closed || state.db == nil {
		return 0, &storage.StoreError{Code: storage.Closed}
	}
	journal, found, err := s.readSourceErasureLocked(ctx)
	if err != nil {
		return 0, err
	}
	durable := found
	defer func() {
		if !durable {
			state.deletionPending.Store(false)
		}
	}()
	cutoffNanos := int64(0)
	if cutoff != nil {
		cutoffNanos = cutoff.UTC().UnixNano()
	}
	if found && (!journal.CaptureScope || journal.Family != familyID || journal.Cutoff != cutoffNanos || journal.HasCutoff != (cutoff != nil) || journal.Inclusive != inclusive) {
		return 0, ErrDeletionPending
	}
	if !found {
		journal = sourceErasure{CaptureScope: true, Family: familyID, Cutoff: cutoffNanos, HasCutoff: cutoff != nil, Inclusive: inclusive, Phase: 12}
		journal.SourceID = journal.capturePlanID()
		if err := s.prepareCaptureErasureLocked(ctx, journal, prepare); err != nil {
			return 0, err
		}
		if err := s.updateLocked(ctx, func(tx *Tx) error {
			if familyID != "" {
				tx.mu.Lock()
				err := tx.putDeletionCutoffLocked(Tombstone{SourceID: familyID, At: cutoff.UTC()})
				tx.mu.Unlock()
				if err != nil {
					return err
				}
			} else if cutoff != nil {
				tx.mu.Lock()
				err := tx.putCaptureRetentionLocked(cutoff.UTC())
				tx.mu.Unlock()
				if err != nil {
					return err
				}
			}
			return tx.PutIndexRecord(erasureNamespace, erasureJournalID, mustJSON(journal))
		}); err != nil {
			return 0, err
		}
		durable = true
	}
	_, err = s.runSourceErasureLocked(ctx, &journal, afterStep)
	return int(journal.Removed), err
}

func (s *Store) captureErasureViewLocked(ctx context.Context, fn func(bolt.Reader) error) error {
	var inner error
	err := s.state.db.View(ctx, func(r bolt.Reader) error {
		inner = fn(r)
		if inner != nil {
			return errAbortTransaction
		}
		return nil
	})
	if inner != nil {
		return inner
	}
	return mapStorePhysicalError(err)
}

func (s *Store) prepareCaptureErasureLocked(ctx context.Context, j sourceErasure, prepare func(string, []tasks.Item) error) error {
	if prepare == nil {
		return nil
	}
	for after := ""; ; {
		var id string
		var selected bool
		if err := s.captureErasureViewLocked(ctx, func(r bolt.Reader) error {
			var err error
			id, err = s.captureNextEditionReader(r, j.Family, after)
			if err != nil || id == "" {
				return err
			}
			edition, summary, found, err := s.captureEditionReader(r, id)
			if err != nil {
				return err
			}
			if !found || j.Family != "" && summary.Header().FamilyID() != j.Family {
				return ErrCorrupt
			}
			selected = j.selects(edition.LastSeen)
			return nil
		}); err != nil {
			return err
		}
		if id == "" {
			return nil
		}
		if selected {
			if err := s.prepareSourceErasureLocked(ctx, id, prepare); err != nil {
				return err
			}
		}
		after = id
	}
}

func (s *Store) captureNextEditionReader(r bolt.Reader, family, after string) (string, error) {
	if after == "" {
		key := "published"
		if family != "" {
			key = "family:" + family
		}
		root, err := s.captureCatalogReader(r, key)
		return root.Head, err
	}
	edition, summary, found, err := s.captureEditionReader(r, after)
	if err != nil {
		return "", err
	}
	if !found || family != "" && summary.Header().FamilyID() != family {
		return "", ErrCorrupt
	}
	if family != "" {
		return edition.FamilyNext, nil
	}
	return edition.Next, nil
}

func (tx *Tx) capturePlanErasureStep(j *sourceErasure) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if !j.CaptureScope {
		return ErrCorrupt
	}
	id, err := tx.store.captureNextEditionReader(tx.writer, j.Family, j.CaptureAfter)
	if err != nil {
		return err
	}
	if id == "" {
		j.Phase = 13
		j.CaptureAfter = ""
		return nil
	}
	r, summary, found, err := tx.store.captureEditionReader(tx.writer, id)
	if err != nil {
		return err
	}
	if !found || j.Family != "" && summary.Header().FamilyID() != j.Family {
		return ErrCorrupt
	}
	if !j.selects(r.LastSeen) {
		j.CaptureAfter = id
		return nil
	}
	// Retention permanently fences the selected edition without treating an
	// equal-time real capture as expired. Family forget already fenced all IDs.
	if j.Family == "" && j.HasCutoff {
		at := time.Unix(0, j.Cutoff).UTC()
		if !j.Inclusive {
			at = at.Add(-time.Nanosecond)
		}
		if !validCaptureTime(at) {
			return ErrCorrupt
		}
		if err := tx.putDeletionCutoffLocked(Tombstone{SourceID: id, At: at}); err != nil {
			return err
		}
	}
	j.SourceID, j.Phase = id, 0
	return nil
}

func (tx *Tx) captureErasePendingStep(j *sourceErasure) (bool, error) {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if !j.CaptureScope {
		return false, ErrCorrupt
	}
	nonce, err := tx.store.captureNextStageReader(tx.writer, j.StageAfter)
	if err != nil || nonce == "" {
		return nonce == "", err
	}
	_, header, found, err := tx.store.captureStageReader(tx.writer, nonce)
	if err != nil {
		return false, err
	}
	if !found {
		return false, ErrCorrupt
	}
	if j.Family != "" && header.FamilyID() != j.Family || !j.selects(header.CapturedAt().UnixNano()) {
		j.StageAfter = nonce
		return false, nil
	}
	_, err = tx.captureDiscardStageStepLocked(nonce)
	return false, err
}
