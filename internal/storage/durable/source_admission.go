package durable

import (
	"bytes"
	"time"

	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

// sourceAdmissionReader proves authenticated identity, publication membership
// and current presence from small receipts in the caller's transaction. It
// deliberately does not hydrate arbitrary source bodies. Actual evidence reads
// still authenticate ciphertext, content IDs and capture-part chains in full.
func (s *Store) sourceAdmissionReader(reader bolt.Reader, id string) (time.Time, bool, error) {
	// Prefer the compact published-part namespace. Probing raw primary/order
	// presence first costs two unnecessary physical operations for every part;
	// distinct historical editions can otherwise exhaust the transaction.
	mapping, found, err := s.captureIndexPartReader(reader, id)
	if err != nil {
		return time.Time{}, false, err
	}
	if found {
		return s.captureSourceAdmissionReader(reader, mapping)
	}
	return s.observationAdmissionReader(reader, id)
}

func (s *Store) observationAdmissionReader(reader bolt.Reader, id string) (time.Time, bool, error) {
	recordKey := s.logicalKey(groupRecord, kindObservation, "", id)
	defer clear(recordKey)
	orderKey := s.logicalKey(groupOrder, kindObservation, "", id)
	defer clear(orderKey)
	raw, indexed, err := reader.Get(orderKey)
	if err != nil {
		return time.Time{}, false, mapStorePhysicalError(err)
	}
	defer clear(raw)
	present, err := reader.Has(recordKey)
	if err != nil {
		return time.Time{}, false, mapStorePhysicalError(err)
	}
	if indexed != present {
		return time.Time{}, false, ErrCorrupt
	}
	if indexed {
		_, at, expectedKey, err := s.decodeSizeEntry(orderKey, raw)
		defer clear(expectedKey)
		if err != nil || !bytes.Equal(expectedKey, recordKey) {
			return time.Time{}, false, ErrCorrupt
		}
		// Legacy v1 receipts intentionally retain an unknown first-capture time.
		return at, true, nil
	}
	return time.Time{}, false, nil
}

func (s *Store) captureSourceAdmissionReader(reader bolt.Reader, mapping captureIndexPart) (time.Time, bool, error) {
	edition, summary, live, err := s.captureLiveReader(reader, mapping.EditionID)
	if err != nil || !live {
		return time.Time{}, false, err
	}
	if edition.Publication != mapping.Publication {
		return time.Time{}, false, nil
	}
	if mapping.Index >= summary.PartCount() {
		return time.Time{}, false, ErrCorrupt
	}
	if mapping.Range && (mapping.LastPart >= summary.PartCount() || mapping.End > summary.Bytes()) {
		return time.Time{}, false, ErrCorrupt
	}
	// The mapping is a membership receipt created only after full part/chain
	// verification. IndexPart is derived migration progress, not liveness.
	key := s.logicalKey(groupRecord, kindIndex, "index:"+captureNS, capturePartKey(edition.Owner, mapping.Index))
	defer clear(key)
	present, err := reader.Has(key)
	if err != nil {
		return time.Time{}, false, mapStorePhysicalError(err)
	}
	if !present {
		return time.Time{}, false, ErrCorrupt
	}
	return summary.Header().CapturedAt(), true, nil
}
