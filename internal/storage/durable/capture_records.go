package durable

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

// Retain the original encrypted namespace for compatibility with saved captures.
const captureNS = "documents-v1"
const captureMetaBytes = 16 << 10
const captureStoredPartBytes = capture.MaxAuthenticatedCapturePartBytes + 4096
const captureVisitRows = 64

type captureCatalog struct {
	Version    int
	Head, Tail string
	Count      int64
}
type captureStageRecord struct {
	Version                 int
	Header                  json.RawMessage
	Root                    string
	Parts, Bytes, MaxInput  int64
	Prev, Next, PublishedID string
	Cleaning                bool  `json:",omitempty"`
	Cleaned                 int64 `json:",omitempty"`
}
type captureStoredPart struct {
	Version       int
	Before, After string
	Part          json.RawMessage
}
type captureEditionRecord struct {
	Version                                   int
	Summary                                   json.RawMessage
	Owner, Prev, Next, FamilyPrev, FamilyNext string
	Publication                               string
	LastSeen, Visits                          int64
	IndexVersion                              int    `json:",omitempty"`
	IndexPart                                 int64  `json:",omitempty"`
	IndexVisits                               int64  `json:",omitempty"`
	IndexQueued                               bool   `json:",omitempty"`
	IndexPrev                                 string `json:",omitempty"`
	IndexNext                                 string `json:",omitempty"`
	TimelineVersion                           int    `json:",omitempty"`
}
type captureReceipt struct {
	Version             int
	EditionID, FamilyID string
	At, Index           int64
}
type captureVisitRow struct {
	CaptureID string
	At        int64
}
type captureVisitRecord struct {
	Version int
	Rows    []captureVisitRow
}

func capturePartKey(nonce string, index int64) string {
	return fmt.Sprintf("part:%s:%016x", nonce, index)
}
func captureVisitKey(id string, page int64) string { return fmt.Sprintf("visits:%s:%016x", id, page) }
func captureOptionalID(id string) bool             { return id == "" || validHash(id) }
func (tx *Tx) captureOpenStageLocked(header capture.CaptureHeader, encoded []byte) (captureStageRecord, error) {
	var empty captureStageRecord
	if err := tx.checkCaptureAdmissionLocked(header); err != nil {
		return empty, err
	}
	stage, _, found, err := tx.store.captureStageReader(tx.writer, header.CaptureID())
	if err != nil {
		return empty, err
	}
	if !found {
		if _, closed, err := tx.store.captureReceiptReader(tx.writer, header.CaptureID()); err != nil {
			return empty, err
		} else if closed {
			return empty, ErrCaptureClosed
		}
		return empty, ErrCaptureConflict
	}
	if stage.PublishedID != "" || stage.Cleaning {
		return empty, ErrCaptureClosed
	}
	if !bytes.Equal(stage.Header, encoded) {
		return empty, ErrCaptureConflict
	}
	return stage, nil
}
func (s *Store) captureReadJSON(reader bolt.Reader, key string, maxBytes int, out any) (bool, error) {
	encoded, found, err := s.readIndexValueReader(reader, captureNS, key, maxBytes)
	if err != nil || !found {
		return found, err
	}
	defer clear(encoded)
	if decodeCanonicalJSON(encoded, out) != nil {
		return false, ErrCorrupt
	}
	return true, nil
}
func (tx *Tx) capturePutJSONLocked(key string, value any, maxBytes int) error {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maxBytes {
		return &storage.StoreError{Code: storage.Invalid}
	}
	defer clear(encoded)
	return tx.putIndexValueLocked(captureNS, key, encoded)
}
func (s *Store) captureCatalogReader(reader bolt.Reader, key string) (captureCatalog, error) {
	r := captureCatalog{Version: 1}
	found, err := s.captureReadJSON(reader, key, captureMetaBytes, &r)
	if err != nil {
		return r, err
	}
	if !found {
		return r, nil
	}
	if r.Version != 1 || r.Count < 0 || !captureOptionalID(r.Head) || !captureOptionalID(r.Tail) || r.Count == 0 && (r.Head != "" || r.Tail != "") || r.Count > 0 && (r.Head == "" || r.Tail == "") || r.Count == 1 && r.Head != r.Tail || r.Count > 1 && r.Head == r.Tail {
		return r, ErrCorrupt
	}
	return r, nil
}
func (s *Store) captureStageReader(reader bolt.Reader, nonce string) (captureStageRecord, capture.CaptureHeader, bool, error) {
	var r captureStageRecord
	found, err := s.captureReadJSON(reader, "stage:"+nonce, captureMetaBytes, &r)
	if err != nil || !found {
		return r, capture.CaptureHeader{}, found, err
	}
	h, err := capture.RestoreAuthenticatedCaptureHeader(r.Header)
	if err != nil || h.CaptureID() != nonce || r.Version != 1 || !validHash(r.Root) || r.Parts < 0 || r.Bytes < 0 || r.MaxInput < 0 || r.Cleaned < 0 || r.Cleaned > r.Parts || r.Cleaned > 0 && !r.Cleaning || r.Parts == 0 && (r.Bytes != 0 || r.Root != h.InitialRoot()) || r.Parts > 0 && (r.Bytes < r.Parts || (r.Bytes-1)/capture.MaxCapturePartTextBytes+1 > r.Parts) || !captureOptionalID(r.Prev) || !captureOptionalID(r.Next) || !captureOptionalID(r.PublishedID) || r.Prev == nonce || r.Next == nonce {
		return r, h, false, ErrCorrupt
	}
	return r, h, true, nil
}
func (s *Store) captureEditionReader(reader bolt.Reader, id string) (captureEditionRecord, capture.CaptureSummary, bool, error) {
	var r captureEditionRecord
	found, err := s.captureReadJSON(reader, "edition:"+id, captureMetaBytes, &r)
	if err != nil || !found {
		return r, capture.CaptureSummary{}, found, err
	}
	summary, err := capture.RestoreAuthenticatedCaptureSummary(r.Summary)
	if err != nil || r.Version != 1 || summary.ContentID() != id || r.Owner != summary.Header().CaptureID() || !validHash(r.Owner) || !validHash(r.Publication) || r.Visits < 1 || r.LastSeen < summary.Header().CapturedAt().UnixNano() || !captureOptionalID(r.Prev) || !captureOptionalID(r.Next) || !captureOptionalID(r.FamilyPrev) || !captureOptionalID(r.FamilyNext) || r.Prev == id || r.Next == id || r.FamilyPrev == id || r.FamilyNext == id || !r.validIndex(id, summary.PartCount()) {
		return r, summary, false, ErrCorrupt
	}
	return r, summary, true, nil
}
func (s *Store) captureReceiptReader(reader bolt.Reader, nonce string) (captureReceipt, bool, error) {
	var r captureReceipt
	found, err := s.captureReadJSON(reader, "receipt:"+nonce, captureMetaBytes, &r)
	if err == nil && found && (r.Version != 1 || !validHash(r.EditionID) || !validHash(r.FamilyID) || r.Index < 0) {
		err = ErrCorrupt
	}
	return r, found, err
}
func (s *Store) captureLiveReader(reader bolt.Reader, id string) (captureEditionRecord, capture.CaptureSummary, bool, error) {
	r, summary, found, err := s.captureEditionReader(reader, id)
	if err != nil || !found {
		return r, summary, found, err
	}
	for _, source := range []string{id, summary.Header().FamilyID()} {
		covered, err := s.deletionCoversReader(reader, source, time.Unix(0, r.LastSeen).UTC())
		if err != nil {
			return r, summary, false, err
		}
		if covered {
			return r, summary, false, nil
		}
	}
	if covered, err := s.captureRetentionCoversReader(reader, time.Unix(0, r.LastSeen).UTC()); err != nil || covered {
		return r, summary, false, err
	}
	return r, summary, true, nil
}
func (tx *Tx) captureLinkStageLocked(nonce string, stage captureStageRecord) error {
	s := tx.store
	root, err := s.captureCatalogReader(tx.writer, "pending")
	if err != nil {
		return err
	}
	if root.Count == math.MaxInt64 {
		return ErrCorrupt
	}
	stage.Prev = root.Tail
	if root.Tail != "" {
		tail, _, found, err := s.captureStageReader(tx.writer, root.Tail)
		if err != nil {
			return err
		}
		if !found || tail.Next != "" {
			return ErrCorrupt
		}
		tail.Next = nonce
		if err := tx.capturePutJSONLocked("stage:"+root.Tail, tail, captureMetaBytes); err != nil {
			return err
		}
	} else {
		root.Head = nonce
	}
	root.Tail = nonce
	root.Count++
	if err := tx.capturePutJSONLocked("stage:"+nonce, stage, captureMetaBytes); err != nil {
		return err
	}
	return tx.capturePutJSONLocked("pending", root, captureMetaBytes)
}
func (tx *Tx) captureUnlinkStageLocked(nonce string, stage captureStageRecord) error {
	s := tx.store
	root, err := s.captureCatalogReader(tx.writer, "pending")
	if err != nil {
		return err
	}
	if root.Count == 0 {
		return ErrCorrupt
	}
	if stage.Prev != "" {
		prev, _, found, err := s.captureStageReader(tx.writer, stage.Prev)
		if err != nil {
			return err
		}
		if !found || prev.Next != nonce {
			return ErrCorrupt
		}
		prev.Next = stage.Next
		if err := tx.capturePutJSONLocked("stage:"+stage.Prev, prev, captureMetaBytes); err != nil {
			return err
		}
	} else {
		if root.Head != nonce {
			return ErrCorrupt
		}
		root.Head = stage.Next
	}
	if stage.Next != "" {
		next, _, found, err := s.captureStageReader(tx.writer, stage.Next)
		if err != nil {
			return err
		}
		if !found || next.Prev != nonce {
			return ErrCorrupt
		}
		next.Prev = stage.Prev
		if err := tx.capturePutJSONLocked("stage:"+stage.Next, next, captureMetaBytes); err != nil {
			return err
		}
	} else {
		if root.Tail != nonce {
			return ErrCorrupt
		}
		root.Tail = stage.Prev
	}
	root.Count--
	if err := tx.capturePutJSONLocked("pending", root, captureMetaBytes); err != nil {
		return err
	}
	return tx.deleteIndexValueLocked(captureNS, "stage:"+nonce)
}
func (tx *Tx) captureLinkEditionLocked(id, familyID string, r *captureEditionRecord) error {
	s := tx.store
	for _, family := range []bool{false, true} {
		key := "published"
		if family {
			key = "family:" + familyID
		}
		root, err := s.captureCatalogReader(tx.writer, key)
		if err != nil {
			return err
		}
		if root.Count == math.MaxInt64 {
			return ErrCorrupt
		}
		if family {
			r.FamilyPrev = root.Tail
		} else {
			r.Prev = root.Tail
		}
		if root.Tail != "" {
			tail, tailSummary, found, err := s.captureEditionReader(tx.writer, root.Tail)
			if err != nil {
				return err
			}
			if !found {
				return ErrCorrupt
			}
			if family {
				if tail.FamilyNext != "" || tailSummary.Header().FamilyID() != familyID {
					return ErrCorrupt
				}
				tail.FamilyNext = id
			} else {
				if tail.Next != "" {
					return ErrCorrupt
				}
				tail.Next = id
			}
			if err := tx.capturePutJSONLocked("edition:"+root.Tail, tail, captureMetaBytes); err != nil {
				return err
			}
		} else {
			root.Head = id
		}
		root.Tail = id
		root.Count++
		if err := tx.capturePutJSONLocked(key, root, captureMetaBytes); err != nil {
			return err
		}
	}
	return nil
}
