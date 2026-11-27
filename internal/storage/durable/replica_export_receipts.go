package durable

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const replicaExportNS = "replica-export-v1"

func replicaGeneration(value string) ([]byte, bool) {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || value != strings.ToLower(value) {
		return nil, false
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return decoded, err == nil && len(decoded) == 16
}

func replicaObservationRevision(value []byte) [32]byte { return sha256.Sum256(value) }

func replicaReceiptID(recordKey []byte) string { return hex.EncodeToString(recordKey) }

func (s *Store) replicaReceiptReader(reader bolt.Reader, generation string, recordKey []byte, revision [32]byte) (bool, error) {
	b, found, err := s.readIndexValueReader(reader, replicaExportNS, replicaReceiptID(recordKey), 48)
	defer clear(b)
	if err != nil || !found {
		return false, err
	}
	if len(b) != 48 {
		return false, ErrCorrupt
	}
	gen, _ := replicaGeneration(generation)
	return bytes.Equal(b[:16], gen) && bytes.Equal(b[16:], revision[:]), nil
}

func (s *Store) replicaObservationCurrentReader(reader bolt.Reader, item ExportedObservation) (string, bool, error) {
	o, err := capture.RestoreAuthenticatedObservation(item.Record)
	if err != nil || !validCaptureTime(item.CapturedAt) {
		return "", false, &storage.StoreError{Code: storage.Invalid}
	}
	id := o.ContentID()
	key := s.logicalKey(groupOrder, kindObservation, "", id)
	defer clear(key)
	b, found, err := reader.Get(key)
	defer clear(b)
	if err != nil {
		return "", false, mapStorePhysicalError(err)
	}
	if !found {
		return id, false, nil
	}
	_, at, recordKey, err := s.decodeSizeEntry(key, b)
	defer clear(recordKey)
	if err != nil {
		return "", false, err
	}
	return id, at.Equal(item.CapturedAt) && replicaObservationRevision(b) == item.Revision, nil
}

// CurrentReplicaObservations revalidates sealed-but-unpublished work against
// the authenticated current source revision. Deleted or replaced data cannot
// be republished by replaying a local outbox after restart.
func (s *Store) CurrentReplicaObservations(ctx context.Context, items []ExportedObservation) ([]bool, error) {
	if ctx == nil || len(items) > 1<<16 {
		return nil, &storage.StoreError{Code: storage.Invalid}
	}
	current := make([]bool, len(items))
	for start := 0; start < len(items); start += exportFetchBatch {
		err := s.view(ctx, func(reader bolt.Reader) error {
			for i := start; i < min(start+exportFetchBatch, len(items)); i++ {
				_, valid, err := s.replicaObservationCurrentReader(reader, items[i])
				if err != nil {
					return err
				}
				current[i] = valid
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return current, nil
}

// AcknowledgeReplicaExport checkpoints only exact source revisions. Callers
// acknowledge local captures only after their immutable segment is published;
// importedOnly admits the separate proof that the source came from a peer.
func (s *Store) AcknowledgeReplicaExport(ctx context.Context, generation string, items []ExportedObservation, importedOnly bool) error {
	gen, ok := replicaGeneration(generation)
	if ctx == nil || !ok || len(items) > 1<<16 {
		return &storage.StoreError{Code: storage.Invalid}
	}
	var imported map[string]bool
	if importedOnly {
		var ids []string
		for _, item := range items {
			o, err := capture.RestoreAuthenticatedObservation(item.Record)
			if err != nil {
				return &storage.StoreError{Code: storage.Invalid}
			}
			ids = append(ids, o.ContentID())
		}
		var err error
		imported, err = s.ImportedSet(ctx, ids)
		if err != nil {
			return err
		}
	}
	// Receipt commits can be retried independently: the caller retains its
	// immutable published outbox until every batch and the state commit succeed.
	// Each item costs one current-order read and a receipt read/write, plus the
	// transaction manifest. Keep each transaction below the physical op budget.
	const receiptBatch = 128
	for start := 0; start < len(items); start += receiptBatch {
		if err := s.Update(ctx, func(tx *Tx) error {
			tx.mu.Lock()
			defer tx.mu.Unlock()
			for _, item := range items[start:min(start+receiptBatch, len(items))] {
				id, current, err := s.replicaObservationCurrentReader(tx.writer, item)
				if err != nil {
					return err
				}
				if !current || importedOnly && !imported[id] {
					continue
				}
				key := s.logicalKey(groupRecord, kindObservation, "", id)
				value := append(append([]byte(nil), gen...), item.Revision[:]...)
				err = tx.putIndexValueLocked(replicaExportNS, replicaReceiptID(key), value)
				clear(key)
				clear(value)
				if err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (tx *Tx) removeReplicaReceiptLocked(sourceID string) error {
	key := tx.store.logicalKey(groupRecord, kindObservation, "", sourceID)
	defer clear(key)
	id := replicaReceiptID(key)
	value, found, err := tx.readIndexValueLocked(replicaExportNS, id, 48)
	defer clear(value)
	if err != nil || !found {
		return err
	}
	if len(value) != 48 {
		return ErrCorrupt
	}
	receiptKey := tx.store.logicalKey(groupRecord, kindIndex, "index:"+replicaExportNS, id)
	defer clear(receiptKey)
	return tx.deleteEntryLocked(receiptKey)
}
