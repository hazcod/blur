package durable

import (
	"context"
	"time"

	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"github.com/hazcod/blur/internal/tasks"
)

// ForgetCaptureFamily removes older editions and abandons older capture jobs.
// prepare must not reenter storage and runs before the resumable intent commits.
func (s *Store) ForgetCaptureFamily(ctx context.Context, familyID string, cutoff time.Time, prepare func(string, []tasks.Item) error) (int, error) {
	if !validHash(familyID) || !validCaptureTime(cutoff) {
		return 0, &storage.StoreError{Code: storage.Invalid}
	}
	return s.deleteCapturesWithHook(ctx, familyID, &cutoff, true, prepare, nil)
}

// DeleteCapturesBefore selects strict-before retention, or all captures when
// cutoff is nil. The coordinator records the all-source fence before delete-all.
func (s *Store) DeleteCapturesBefore(ctx context.Context, cutoff *time.Time, prepare func(string, []tasks.Item) error) (int, error) {
	return s.deleteCapturesWithHook(ctx, "", cutoff, false, prepare, nil)
}

// DiscardCaptureStage abandons only a private capture payload. A published
// edition's canonical payload is not a stage and cannot be discarded here.
func (s *Store) DiscardCaptureStage(ctx context.Context, captureID string) error {
	return s.discardCaptureStageWithHook(ctx, captureID, nil)
}

func (s *Store) discardCaptureStageWithHook(ctx context.Context, captureID string, afterStep func() error) error {
	if ctx == nil || !validHash(captureID) {
		return &storage.StoreError{Code: storage.Invalid}
	}
	for {
		done := false
		err := s.captureUpdate(ctx, func(tx *Tx) error {
			var err error
			done, err = tx.captureDiscardStageStepLocked(captureID)
			return err
		})
		if err != nil || done {
			return err
		}
		if afterStep != nil {
			if err := afterStep(); err != nil {
				return err
			}
		}
	}
}

// Closing a stage and advancing deletion progress are separate bounded commits.
// The original count/root remain intact to validate its authenticated metadata.
func (tx *Tx) captureDiscardStageStepLocked(nonce string) (bool, error) {
	s := tx.store
	stage, header, found, err := s.captureStageReader(tx.writer, nonce)
	if err != nil || !found {
		return !found, err
	}
	if receipt, found, err := s.captureReceiptReader(tx.writer, nonce); err != nil {
		return false, err
	} else if found && (receipt.EditionID != stage.PublishedID || receipt.FamilyID != header.FamilyID() || receipt.At != header.CapturedAt().UnixNano()) {
		// In particular, a canonical owner's receipt with no PublishedID on
		// the stage signals a broken publication/catalog invariant. Fail before
		// removing anything, rather than deleting the committed payload.
		return false, ErrCorrupt
	}
	if stage.PublishedID != "" {
		edition, _, found, err := s.captureEditionReader(tx.writer, stage.PublishedID)
		if err != nil {
			return false, err
		}
		if !found || edition.Owner == nonce {
			return false, ErrCorrupt
		}
	}
	if !stage.Cleaning {
		stage.Cleaning = true
		return false, tx.capturePutJSONLocked("stage:"+nonce, stage, captureMetaBytes)
	}
	if stage.Cleaned < stage.Parts {
		if err := tx.captureDeletePartLocked(nonce, stage.Cleaned); err != nil {
			return false, err
		}
		stage.Cleaned++
		return false, tx.capturePutJSONLocked("stage:"+nonce, stage, captureMetaBytes)
	}
	return true, tx.captureUnlinkStageLocked(nonce, stage)
}

func (tx *Tx) captureDeletePartLocked(nonce string, index int64) error {
	key := capturePartKey(nonce, index)
	// A missing unaccounted part is corruption, not successful cleanup.
	logical := tx.store.logicalKey(groupRecord, kindIndex, "index:"+captureNS, key)
	found, err := tx.writer.Has(logical)
	clear(logical)
	if err != nil {
		return mapStorePhysicalError(err)
	}
	if !found {
		return ErrCorrupt
	}
	return tx.deleteIndexValueLocked(captureNS, key)
}

func (s *Store) recoverCaptureStaging(ctx context.Context) error {
	for {
		var nonce string
		if err := s.view(ctx, func(r bolt.Reader) error {
			root, err := s.captureCatalogReader(r, "pending")
			nonce = root.Head
			return err
		}); err != nil {
			return err
		}
		if nonce == "" {
			return nil
		}
		if err := s.DiscardCaptureStage(ctx, nonce); err != nil {
			return err
		}
	}
}

func (tx *Tx) captureErasePartsStep(j *sourceErasure) (bool, error) {
	tx.mu.Lock()
	r, summary, found, err := tx.store.captureEditionReader(tx.writer, j.SourceID)
	tx.mu.Unlock()
	if err != nil || !found {
		return !found, err
	}
	if j.Part > summary.PartCount() {
		return false, ErrCorrupt
	}
	if j.Part == summary.PartCount() {
		if j.PartPhase != 0 || j.PartAfter != "" {
			return false, ErrCorrupt
		}
		return true, nil
	}
	if done, err := tx.eraseCaptureRangeStep(j, j.SourceID, r); err != nil || !done {
		return false, err
	}
	// The same bounded projection phases apply to the genuine part identity.
	// Keep their cursor in the parent journal, so a crash cannot advance past
	// retained claims or lose the authenticated mapping needed to find them.
	if j.PartPhase < 8 {
		part := sourceErasure{SourceID: capturePartSourceID(j.SourceID, r.Publication, j.Part), Phase: j.PartPhase, After: j.PartAfter}
		if _, err := tx.eraseSourceStep(&part); err != nil {
			return false, err
		}
		j.PartPhase, j.PartAfter = part.Phase, part.After
		return false, nil
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if done, err := tx.eraseCapturePartIndexLocked(j.SourceID, r, j.Part); err != nil || !done {
		return false, err
	}
	if err := tx.captureDeletePartLocked(r.Owner, j.Part); err != nil {
		return false, err
	}
	j.Part++
	j.PartPhase, j.PartAfter = 0, ""
	return false, nil
}

func (tx *Tx) captureEraseVisitsStep(j *sourceErasure) (bool, error) {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	r, summary, found, err := tx.store.captureEditionReader(tx.writer, j.SourceID)
	if err != nil || !found {
		return !found, err
	}
	pages := (r.Visits-1)/captureVisitRows + 1
	if j.VisitPage > pages {
		return false, ErrCorrupt
	}
	if j.VisitPage == pages {
		if j.VisitRow != 0 {
			return false, ErrCorrupt
		}
		return true, nil
	}
	page, found, err := tx.store.captureVisitReader(tx.writer, j.SourceID, j.VisitPage)
	if err != nil {
		return false, err
	}
	if !found || int64(len(page.Rows)) != min(captureVisitRows, r.Visits-j.VisitPage*captureVisitRows) {
		return false, ErrCorrupt
	}

	if j.VisitRow < 0 || j.VisitRow >= int64(len(page.Rows)) {
		return false, ErrCorrupt
	}
	end := min(int(j.VisitRow)+8, len(page.Rows))
	for i := int(j.VisitRow); i < end; i++ {
		row := page.Rows[i]
		ordinal := j.VisitPage*captureVisitRows + int64(i)
		receipt, found, err := tx.store.captureReceiptReader(tx.writer, row.CaptureID)
		if err != nil {
			return false, err
		}
		if !found || receipt.EditionID != j.SourceID || receipt.FamilyID != summary.Header().FamilyID() || receipt.At != row.At || receipt.Index != ordinal {
			return false, ErrCorrupt
		}
		if err := tx.eraseCaptureVisitIndexLocked(j.SourceID, r, row, ordinal); err != nil {
			return false, err
		}
		if err := tx.deleteIndexValueLocked(captureNS, "receipt:"+row.CaptureID); err != nil {
			return false, err
		}
	}
	j.VisitRow = int64(end)
	if end == len(page.Rows) {
		if err := tx.deleteIndexValueLocked(captureNS, captureVisitKey(j.SourceID, j.VisitPage)); err != nil {
			return false, err
		}
		j.VisitPage++
		j.VisitRow = 0
	}
	return false, nil
}

func (tx *Tx) captureEraseDuplicatesStep(j *sourceErasure) (bool, error) {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if _, _, found, err := tx.store.captureEditionReader(tx.writer, j.SourceID); err != nil || !found {
		return !found, err
	}
	nonce, err := tx.store.captureNextStageReader(tx.writer, j.StageAfter)
	if err != nil || nonce == "" {
		return nonce == "", err
	}
	stage, _, found, err := tx.store.captureStageReader(tx.writer, nonce)
	if err != nil {
		return false, err
	}
	if !found {
		return false, ErrCorrupt
	}
	if stage.PublishedID != j.SourceID {
		j.StageAfter = nonce
		return false, nil
	}
	_, err = tx.captureDiscardStageStepLocked(nonce)
	return false, err
}

func (s *Store) captureNextStageReader(r bolt.Reader, after string) (string, error) {
	if after == "" {
		root, err := s.captureCatalogReader(r, "pending")
		return root.Head, err
	}
	stage, _, found, err := s.captureStageReader(r, after)
	if err != nil {
		return "", err
	}
	if !found {
		return "", ErrCorrupt
	}
	return stage.Next, nil
}

func (tx *Tx) captureUnlinkEdition(id string) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	r, summary, found, err := tx.store.captureEditionReader(tx.writer, id)
	if err != nil || !found {
		return err
	}
	if err := tx.unlinkCaptureIndexReferencesLocked(id, &r); err != nil {
		return err
	}
	if r.IndexRangeWork != nil && r.IndexRangeWork.CoreTerms > 0 {
		if err := tx.deleteIndexValueLocked(captureNS, captureRangeCoreKey(id, r.Publication)); err != nil {
			return err
		}
	}
	if r.IndexRangeQueued > 0 {
		if err := tx.deleteIndexValueLocked(captureNS, captureRangeQueueKey(id, r.Publication)); err != nil {
			return err
		}
	}
	for _, family := range []bool{false, true} {
		key, prev, next := "published", r.Prev, r.Next
		if family {
			key, prev, next = "family:"+summary.Header().FamilyID(), r.FamilyPrev, r.FamilyNext
		}
		root, err := tx.store.captureCatalogReader(tx.writer, key)
		if err != nil {
			return err
		}
		if root.Count < 1 {
			return ErrCorrupt
		}
		for _, before := range []bool{true, false} {
			neighborID := next
			if before {
				neighborID = prev
			}
			if neighborID == "" {
				if before {
					if root.Head != id {
						return ErrCorrupt
					}
					root.Head = next
				} else {
					if root.Tail != id {
						return ErrCorrupt
					}
					root.Tail = prev
				}
				continue
			}
			n, ns, found, err := tx.store.captureEditionReader(tx.writer, neighborID)
			if err != nil {
				return err
			}
			if !found || family && ns.Header().FamilyID() != summary.Header().FamilyID() {
				return ErrCorrupt
			}
			link := &n.Prev
			if before {
				link = &n.Next
			}
			if family {
				link = &n.FamilyPrev
				if before {
					link = &n.FamilyNext
				}
			}
			if *link != id {
				return ErrCorrupt
			}
			if before {
				*link = next
			} else {
				*link = prev
			}
			if err := tx.capturePutJSONLocked("edition:"+neighborID, n, captureMetaBytes); err != nil {
				return err
			}
		}
		root.Count--
		if root.Count == 0 {
			if root.Head != "" || root.Tail != "" {
				return ErrCorrupt
			}
			if err := tx.deleteIndexValueLocked(captureNS, key); err != nil {
				return err
			}
		} else if err := tx.capturePutJSONLocked(key, root, captureMetaBytes); err != nil {
			return err
		}
	}
	return tx.deleteIndexValueLocked(captureNS, "edition:"+id)
}
