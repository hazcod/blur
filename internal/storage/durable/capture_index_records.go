package durable

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"

	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const captureIndexVersion = 1
const captureIndexBackfillKey = "index-backfill"
const captureIndexPendingKey = "index-pending"

type captureIndexBackfill struct {
	Version int
	Next    string
}
type captureIndexPart struct {
	Version                int
	EditionID, Publication string
	Index                  int64
	Bytes                  int
}

func capturePartSourceID(id, publication string, index int64) string {
	var ordinal [8]byte
	binary.BigEndian.PutUint64(ordinal[:], uint64(index))
	h := sha256.New()
	h.Write([]byte("blur.capture.part-source.v1\x00"))
	h.Write([]byte(id))
	h.Write([]byte(publication))
	h.Write(ordinal[:])
	return hex.EncodeToString(h.Sum(nil))
}
func (r captureEditionRecord) validIndex(id string, parts int64) bool {
	if r.TimelineVersion < 0 || r.TimelineVersion > 1 {
		return false
	}
	if r.IndexVersion == 0 {
		return r.IndexPart == 0 && r.IndexVisits == 0 && !r.IndexQueued && r.IndexPrev == "" && r.IndexNext == ""
	}
	return r.IndexVersion == captureIndexVersion && r.IndexPart >= 0 && r.IndexPart <= parts && r.IndexVisits >= 0 && r.IndexVisits <= r.Visits && captureOptionalID(r.IndexPrev) && captureOptionalID(r.IndexNext) && r.IndexPrev != id && r.IndexNext != id && (r.IndexQueued || r.IndexPrev == "" && r.IndexNext == "")
}
func (s *Store) captureIndexBackfillReader(reader bolt.Reader) (captureIndexBackfill, bool, error) {
	var r captureIndexBackfill
	found, err := s.captureReadJSON(reader, captureIndexBackfillKey, 256, &r)
	if err != nil || !found {
		return r, found, err
	}
	if r.Version != captureIndexVersion || !captureOptionalID(r.Next) {
		return r, false, ErrCorrupt
	}
	return r, true, nil
}
func (s *Store) captureIndexPartReader(reader bolt.Reader, id string) (captureIndexPart, bool, error) {
	var r captureIndexPart
	found, err := s.captureReadJSON(reader, "index-part:"+id, 512, &r)
	if err != nil || !found {
		return r, found, err
	}
	if r.Version != captureIndexVersion || !validHash(r.EditionID) || !validHash(r.Publication) || r.Index < 0 || r.Bytes < 1 || r.Bytes > 32<<10 || capturePartSourceID(r.EditionID, r.Publication, r.Index) != id {
		return r, false, ErrCorrupt
	}
	return r, true, nil
}

// Queue membership is part of the authenticated edition descriptor. Publication
// writes only this bounded receipt, never a receipt for every source part.
func (tx *Tx) queueCaptureIndexLocked(id string, r *captureEditionRecord, parts int64) error {
	if r.IndexVersion == 0 {
		r.IndexVersion = captureIndexVersion
	}
	if r.IndexQueued || r.IndexPart == parts && r.IndexVisits == r.Visits {
		return nil
	}
	root, err := tx.store.captureCatalogReader(tx.writer, captureIndexPendingKey)
	if err != nil {
		return err
	}
	if root.Count == math.MaxInt64 || root.Tail == id {
		return ErrCorrupt
	}
	r.IndexPrev = root.Tail
	r.IndexNext = ""
	r.IndexQueued = true
	if root.Tail != "" {
		tail, _, found, err := tx.store.captureEditionReader(tx.writer, root.Tail)
		if err != nil {
			return err
		}
		if !found || !tail.IndexQueued || tail.IndexNext != "" {
			return ErrCorrupt
		}
		tail.IndexNext = id
		if err := tx.capturePutJSONLocked("edition:"+root.Tail, tail, captureMetaBytes); err != nil {
			return err
		}
	} else {
		root.Head = id
	}
	root.Tail = id
	root.Count++
	return tx.capturePutJSONLocked(captureIndexPendingKey, root, captureMetaBytes)
}
func (tx *Tx) unlinkCaptureIndexLocked(id string, r *captureEditionRecord) error {
	if !r.IndexQueued {
		return nil
	}
	root, err := tx.store.captureCatalogReader(tx.writer, captureIndexPendingKey)
	if err != nil {
		return err
	}
	if root.Count < 1 {
		return ErrCorrupt
	}
	for _, before := range []bool{true, false} {
		neighbor := r.IndexNext
		if before {
			neighbor = r.IndexPrev
		}
		if neighbor == "" {
			if before {
				if root.Head != id {
					return ErrCorrupt
				}
				root.Head = r.IndexNext
			} else {
				if root.Tail != id {
					return ErrCorrupt
				}
				root.Tail = r.IndexPrev
			}
			continue
		}
		n, _, found, err := tx.store.captureEditionReader(tx.writer, neighbor)
		if err != nil {
			return err
		}
		if !found || !n.IndexQueued {
			return ErrCorrupt
		}
		if before {
			if n.IndexNext != id {
				return ErrCorrupt
			}
			n.IndexNext = r.IndexNext
		} else {
			if n.IndexPrev != id {
				return ErrCorrupt
			}
			n.IndexPrev = r.IndexPrev
		}
		if err := tx.capturePutJSONLocked("edition:"+neighbor, n, captureMetaBytes); err != nil {
			return err
		}
	}
	root.Count--
	if root.Count == 0 && (root.Head != "" || root.Tail != "") {
		return ErrCorrupt
	}
	r.IndexPrev = ""
	r.IndexNext = ""
	r.IndexQueued = false
	return tx.capturePutJSONLocked(captureIndexPendingKey, root, captureMetaBytes)
}
func (tx *Tx) unlinkCaptureIndexReferencesLocked(id string, r *captureEditionRecord) error {
	if err := tx.unlinkCaptureIndexLocked(id, r); err != nil {
		return err
	}
	backfill, found, err := tx.store.captureIndexBackfillReader(tx.writer)
	if err != nil {
		return err
	}
	if found && backfill.Next == id {
		backfill.Next = r.Next
		return tx.capturePutJSONLocked(captureIndexBackfillKey, backfill, 256)
	}
	return nil
}
