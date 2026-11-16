package durable

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"time"

	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const captureIndexVisit byte = 0x13

type captureIndexedVisit struct {
	Version                int
	CaptureID, Publication string
	At                     int64
}

// The time order is encrypted inside the tree; physical keys remain keyed.
// Inversion orders newest visits first, including times before the Unix epoch.
func captureVisitSourceID(nonce string, at int64) string {
	var key [32]byte
	binary.BigEndian.PutUint64(key[:8], ^(uint64(at) ^ (uint64(1) << 63)))
	digest := sha256.Sum256([]byte(nonce))
	copy(key[8:], digest[:24])
	return hex.EncodeToString(key[:])
}
func captureVisitIndexKey(id, visit string) string { return "visit-index:" + id + ":" + visit }
func (s *Store) captureIndexedVisitReader(reader bolt.Reader, id, visit string) (captureIndexedVisit, bool, error) {
	var r captureIndexedVisit
	found, err := s.captureReadJSON(reader, captureVisitIndexKey(id, visit), 512, &r)
	if err != nil || !found {
		return r, found, err
	}
	if r.Version != captureIndexVersion || !validHash(r.CaptureID) || !validHash(r.Publication) || !validCaptureTime(time.Unix(0, r.At).UTC()) || captureVisitSourceID(r.CaptureID, r.At) != visit {
		return r, false, ErrCorrupt
	}
	return r, true, nil
}
func (tx *Tx) indexCaptureVisitLocked(id, publication, nonce string, at int64) error {
	visit := captureVisitSourceID(nonce, at)
	existing, found, err := tx.store.captureIndexedVisitReader(tx.writer, id, visit)
	if err != nil {
		return err
	}
	expected := captureIndexedVisit{captureIndexVersion, nonce, publication, at}
	if found && existing != expected {
		return ErrCorrupt
	}
	if err := tx.indexCaptureTimelineLocked(id, time.Unix(0, at).UTC()); err != nil {
		return err
	}
	if found {
		return nil
	}
	if _, _, err := tx.capturePartSearch().addSearchIndexPostingLocked(captureIndexVisit, id, visit, 0); err != nil {
		return err
	}
	return tx.capturePutJSONLocked(captureVisitIndexKey(id, visit), expected, 512)
}
func (tx *Tx) eraseCaptureVisitIndexLocked(id string, edition captureEditionRecord, row captureVisitRow, ordinal int64) error {
	if err := tx.removeCaptureTimelineLocked(id, time.Unix(0, row.At).UTC()); err != nil {
		return err
	}
	visit := captureVisitSourceID(row.CaptureID, row.At)
	record, found, err := tx.store.captureIndexedVisitReader(tx.writer, id, visit)
	if err != nil {
		return err
	}
	if !found {
		if edition.IndexVersion != 0 && ordinal < edition.IndexVisits {
			return ErrCorrupt
		}
		return nil // an old visit whose background prefix has not reached it
	}
	if record.Publication != edition.Publication || record.CaptureID != row.CaptureID || record.At != row.At {
		return ErrCorrupt
	}
	scoped := tx.capturePartSearch()
	digest := scoped.store.searchLookupDigest(captureIndexVisit, id)
	if err := scoped.removeSearchIndexTreePostingLocked(captureIndexVisit, digest, visit, 0); err != nil {
		return err
	}
	return tx.deleteIndexValueLocked(captureNS, captureVisitIndexKey(id, visit))
}
func (tx *Tx) indexNextCaptureVisitLocked(id string, r *captureEditionRecord, family string) error {
	if r.IndexVisits >= r.Visits {
		return nil
	}
	ordinal := r.IndexVisits
	page, found, err := tx.store.captureVisitReader(tx.writer, id, ordinal/captureVisitRows)
	if err != nil {
		return err
	}
	slot := int(ordinal % captureVisitRows)
	if !found || slot >= len(page.Rows) {
		return ErrCorrupt
	}
	row := page.Rows[slot]
	receipt, found, err := tx.store.captureReceiptReader(tx.writer, row.CaptureID)
	if err != nil {
		return err
	}
	if !found || receipt.EditionID != id || receipt.FamilyID != family || receipt.At != row.At || receipt.Index != ordinal {
		return ErrCorrupt
	}
	if err := tx.indexCaptureVisitLocked(id, r.Publication, row.CaptureID, row.At); err != nil {
		return err
	}
	r.IndexVisits++
	return nil
}
