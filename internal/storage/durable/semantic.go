package durable

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/semantic"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const (
	semanticNamespace      = "semantic-v1"
	semanticOrderNamespace = "semantic-order-v1"
	semanticMetaNamespace  = "semantic-meta"
	semanticCheckpointID   = "backfill-v1"
	semanticOrderBytes     = 69
)

// The kindIndex order prefix is reserved for the semantic source directory.
// Records still use the ordinary authenticated index-value envelope.
var semanticOrderPrefix = []byte{groupOrder, kindIndex}

var _ semantic.Repository = (*Store)(nil)

// GetObservationWithCaptureTime reads the immutable source and authenticated
// timestamp in one snapshot for incremental, source-linked index work. This
// retains migration baselines for index compatibility; observed-date readers
// additionally use IsObservationTimeKnown.
func (s *Store) GetObservationWithCaptureTime(ctx context.Context, sourceID string) (capture.SanitizedObservation, time.Time, bool, error) {
	if ctx == nil || !validHash(sourceID) {
		return capture.SanitizedObservation{}, time.Time{}, false, &storage.StoreError{Code: storage.Invalid}
	}
	var observation capture.SanitizedObservation
	var at time.Time
	var found bool
	err := s.view(ctx, func(reader bolt.Reader) error {
		var err error
		observation, found, err = s.getObservationReader(reader, sourceID)
		if err != nil || !found {
			return err
		}
		at, err = s.observationCapturedAtReader(reader, sourceID)
		return err
	})
	if err != nil {
		return capture.SanitizedObservation{}, time.Time{}, false, err
	}
	return observation, at, found, nil
}

type semanticOrder struct {
	sourceID, modelKey string
	bytes              int
}

func (s *Store) semanticOrderKey(sourceID string) []byte {
	return s.logicalKey(groupOrder, kindIndex, semanticNamespace, sourceID)
}

// PutSemanticDocument atomically checks immutable source provenance before
// publishing vectors and their encrypted listing metadata. A job that finishes
// after source deletion cannot recreate derived memory.
func (s *Store) PutSemanticDocument(ctx context.Context, document semantic.Document) error {
	if ctx == nil || document.Validate() != nil {
		return &storage.StoreError{Code: storage.Invalid}
	}
	encoded, err := semantic.EncodeDocument(document)
	if err != nil {
		return &storage.StoreError{Code: storage.Invalid}
	}
	defer clear(encoded)
	sourceGone := false
	err = s.Update(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		observation, at, found, err := s.sourceEvidenceReader(tx.writer, document.SourceID)
		if err != nil {
			return tx.failLocked(err)
		}
		if found {
			found, err = s.isCurrentNoteSourceReader(tx.writer, document.SourceID)
			if err != nil {
				return tx.failLocked(err)
			}
		}
		if !found {
			sourceGone = true
			return tx.failLocked(&storage.StoreError{Code: storage.Invalid})
		}
		text := observation.Text()
		if document.AppID != observation.AppID() || document.Domain != observation.Domain() || document.TextBytes != len(text) || !document.CapturedAt.Equal(at) {
			return tx.failLocked(&storage.StoreError{Code: storage.Invalid})
		}
		if document.SkipReason == semantic.SkipSelf && document.AppID != appidentity.BundleID || document.AppID == appidentity.BundleID && document.SkipReason != semantic.SkipSelf || document.SkipReason == semantic.SkipEmpty && strings.TrimSpace(text) != "" {
			return tx.failLocked(&storage.StoreError{Code: storage.Invalid})
		}
		for _, chunk := range document.Chunks {
			if !utf8.ValidString(text[chunk.Start:chunk.End]) {
				return tx.failLocked(&storage.StoreError{Code: storage.Invalid})
			}
		}
		if err := tx.removeSemanticProgressLocked(document.SourceID); err != nil {
			return tx.failLocked(err)
		}
		if err := tx.putIndexValueLocked(semanticNamespace, document.SourceID, encoded); err != nil {
			return tx.failLocked(err)
		}
		orderKey := s.semanticOrderKey(document.SourceID)
		defer clear(orderKey)
		plain := encodeSemanticOrder(semanticOrder{sourceID: document.SourceID, modelKey: document.Model.Key(), bytes: len(encoded)})
		defer clear(plain)
		envelope, err := s.seal(orderKey, semanticOrderNamespace, plain)
		if err != nil {
			return tx.failLocked(err)
		}
		defer clear(envelope)
		return tx.writeEntryLocked(orderKey, envelope, true)
	})
	if sourceGone && err != nil {
		return semantic.ErrSourceGone
	}
	return err
}

func (s *Store) GetSemanticDocument(ctx context.Context, sourceID string) (semantic.Document, bool, error) {
	if ctx == nil || !validHash(sourceID) {
		return semantic.Document{}, false, &storage.StoreError{Code: storage.Invalid}
	}
	var document semantic.Document
	found := false
	err := s.view(ctx, func(reader bolt.Reader) error {
		current, err := s.isCurrentNoteSourceReader(reader, sourceID)
		if err != nil || !current {
			return err
		}
		order, exists, err := s.readSemanticOrder(reader, sourceID)
		if err != nil {
			return err
		}
		if !exists {
			key := s.logicalKey(groupRecord, kindIndex, "index:"+semanticNamespace, sourceID)
			defer clear(key)
			orphan, err := reader.Has(key)
			if err != nil {
				return mapStorePhysicalError(err)
			}
			if orphan {
				return ErrCorrupt
			}
			return nil
		}
		document, err = s.readSemanticDocument(reader, order)
		found = err == nil
		return err
	})
	return document, found, err
}

func (s *Store) readSemanticDocument(reader bolt.Reader, order semanticOrder) (semantic.Document, error) {
	encoded, found, err := s.readIndexValueReader(reader, semanticNamespace, order.sourceID, semantic.MaxDocumentBytes)
	if err != nil {
		return semantic.Document{}, err
	}
	if !found || len(encoded) != order.bytes {
		clear(encoded)
		return semantic.Document{}, ErrCorrupt
	}
	defer clear(encoded)
	document, err := semantic.DecodeDocument(encoded)
	if err != nil || document.SourceID != order.sourceID || document.Model.Key() != order.modelKey {
		return semantic.Document{}, ErrCorrupt
	}
	source, at, exists, err := s.sourceEvidenceReader(reader, document.SourceID)
	if err != nil {
		return semantic.Document{}, err
	}
	if !exists || source.AppID() != document.AppID || source.Domain() != document.Domain || len(source.Text()) != document.TextBytes || !at.Equal(document.CapturedAt) {
		return semantic.Document{}, ErrCorrupt
	}
	for _, chunk := range document.Chunks {
		if !utf8.ValidString(source.Text()[chunk.Start:chunk.End]) {
			return semantic.Document{}, ErrCorrupt
		}
	}
	return document, nil
}

// ListSemanticDocuments pages immutable vector records under one read snapshot.
// Next advances across non-matching model generations too; an empty filtered
// page may therefore have HasMore with a valid continuation cursor.
func (s *Store) ListSemanticDocuments(ctx context.Context, request semantic.PageRequest) (semantic.Page, error) {
	if ctx == nil || request.Limit < 1 || request.Limit > semantic.MaxPageRows || request.MaxBytes < semantic.MaxDocumentBytes || request.MaxBytes > semantic.MaxPageBytes || request.After != "" && !validHash(request.After) || request.ModelKey != "" && !validHash(request.ModelKey) {
		return semantic.Page{}, &storage.StoreError{Code: storage.Invalid}
	}
	var after []byte
	if request.After != "" {
		var err error
		after, err = s.cursorKey(groupOrder, kindIndex, semanticNamespace, request.After)
		if err != nil {
			return semantic.Page{}, err
		}
		defer clear(after)
	}
	page := semantic.Page{Documents: make([]semantic.Document, 0, request.Limit)}
	err := s.view(ctx, func(reader bolt.Reader) error {
		entries, more, err := reader.Scan(semanticOrderPrefix, after, request.Limit, bolt.MaxTransactionBytes-bolt.MaxKeyBytes)
		if err != nil {
			return mapStorePhysicalError(err)
		}
		defer clearBoltEntries(entries)
		page.HasMore = more
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			order, err := s.decodeSemanticOrder(entry.Key, entry.Value)
			if err != nil {
				return err
			}
			current, err := s.isCurrentNoteSourceReader(reader, order.sourceID)
			if err != nil {
				return err
			}
			if !current {
				page.Next = order.sourceID
				continue
			}
			if request.ModelKey != "" && order.modelKey != request.ModelKey {
				page.Next = order.sourceID
				continue
			}
			if order.bytes > request.MaxBytes-page.Bytes {
				page.HasMore = true
				break
			}
			document, err := s.readSemanticDocument(reader, order)
			if err != nil {
				return err
			}
			page.Documents = append(page.Documents, document)
			page.Bytes += order.bytes
			page.Next = order.sourceID
		}
		return nil
	})
	if err != nil {
		return semantic.Page{}, err
	}
	return page, nil
}

func (s *Store) readSemanticOrder(reader bolt.Reader, sourceID string) (semanticOrder, bool, error) {
	key := s.semanticOrderKey(sourceID)
	defer clear(key)
	encoded, found, err := reader.Get(key)
	if err != nil {
		return semanticOrder{}, false, mapStorePhysicalError(err)
	}
	if !found {
		return semanticOrder{}, false, nil
	}
	defer clear(encoded)
	order, err := s.decodeSemanticOrder(key, encoded)
	return order, err == nil, err
}

func (s *Store) decodeSemanticOrder(key, encoded []byte) (semanticOrder, error) {
	if len(encoded) > semanticOrderBytes+1024 {
		return semanticOrder{}, ErrCorrupt
	}
	plain, err := s.open(key, semanticOrderNamespace, encoded)
	if err != nil {
		return semanticOrder{}, ErrCorrupt
	}
	defer clear(plain)
	if len(plain) != semanticOrderBytes || plain[0] != 1 {
		return semanticOrder{}, ErrCorrupt
	}
	order := semanticOrder{sourceID: hex.EncodeToString(plain[1:33]), modelKey: hex.EncodeToString(plain[33:65]), bytes: int(binary.BigEndian.Uint32(plain[65:69]))}
	expected := s.semanticOrderKey(order.sourceID)
	defer clear(expected)
	if !bytes.Equal(key, expected) || order.bytes < 1 || order.bytes > semantic.MaxDocumentBytes {
		return semanticOrder{}, ErrCorrupt
	}
	return order, nil
}

func encodeSemanticOrder(order semanticOrder) []byte {
	encoded := make([]byte, semanticOrderBytes)
	encoded[0] = 1
	_, _ = hex.Decode(encoded[1:33], []byte(order.sourceID))
	_, _ = hex.Decode(encoded[33:65], []byte(order.modelKey))
	binary.BigEndian.PutUint32(encoded[65:69], uint32(order.bytes))
	return encoded
}

func (s *Store) GetSemanticCheckpoint(ctx context.Context, modelKey string) (semantic.Checkpoint, bool, error) {
	if ctx == nil || !validHash(modelKey) {
		return semantic.Checkpoint{}, false, &storage.StoreError{Code: storage.Invalid}
	}
	encoded, found, err := s.readIndexValue(ctx, semanticMetaNamespace, semanticCheckpointID, 512)
	if err != nil || !found {
		return semantic.Checkpoint{}, false, err
	}
	defer clear(encoded)
	var checkpoint semantic.Checkpoint
	if json.Unmarshal(encoded, &checkpoint) != nil || !validSemanticCheckpoint(checkpoint) {
		return semantic.Checkpoint{}, false, ErrCorrupt
	}
	if checkpoint.ModelKey != modelKey {
		return semantic.Checkpoint{}, false, nil
	}
	return checkpoint, true, nil
}

func (s *Store) PutSemanticCheckpoint(ctx context.Context, checkpoint semantic.Checkpoint) error {
	if ctx == nil || !validSemanticCheckpoint(checkpoint) {
		return &storage.StoreError{Code: storage.Invalid}
	}
	return s.Update(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		if checkpoint.After != "" {
			key := s.logicalKey(groupRecord, kindObservation, "", checkpoint.After)
			found, err := tx.writer.Has(key)
			clear(key)
			if err != nil {
				return tx.failLocked(mapStorePhysicalError(err))
			}
			if !found {
				checkpoint.After = ""
				checkpoint.Complete = false
				checkpoint.Indexed = 0
				checkpoint.Skipped = 0
			}
		}
		encoded, err := json.Marshal(checkpoint)
		if err != nil {
			return tx.failLocked(&storage.StoreError{Code: storage.Invalid})
		}
		defer clear(encoded)
		return tx.putIndexValueLocked(semanticMetaNamespace, semanticCheckpointID, encoded)
	})
}

func validSemanticCheckpoint(checkpoint semantic.Checkpoint) bool {
	return validHash(checkpoint.ModelKey) && (checkpoint.After == "" || validHash(checkpoint.After)) && checkpoint.Indexed <= 1<<40 && checkpoint.Skipped <= 1<<40 && storage.ValidCaptureEvidenceCursor(checkpoint.CaptureAfter)
}

func (tx *Tx) removeSemanticSource(sourceID string) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if err := tx.checkLocked(); err != nil {
		return tx.failLocked(err)
	}
	if err := tx.removeSemanticProgressLocked(sourceID); err != nil {
		return err
	}
	_, found, err := tx.store.readSemanticOrder(tx.writer, sourceID)
	if err != nil {
		return err
	}
	vectorKey := tx.store.logicalKey(groupRecord, kindIndex, "index:"+semanticNamespace, sourceID)
	defer clear(vectorKey)
	if found {
		if err := tx.deleteEntryLocked(vectorKey); err != nil {
			return err
		}
		orderKey := tx.store.semanticOrderKey(sourceID)
		defer clear(orderKey)
		if err := tx.deleteEntryLocked(orderKey); err != nil {
			return err
		}
	} else {
		orphan, err := tx.writer.Has(vectorKey)
		if err != nil {
			return mapStorePhysicalError(err)
		}
		if orphan {
			return ErrCorrupt
		}
	}
	encoded, exists, err := tx.readIndexValueLocked(semanticMetaNamespace, semanticCheckpointID, 512)
	if err != nil || !exists {
		return err
	}
	defer clear(encoded)
	var checkpoint semantic.Checkpoint
	if json.Unmarshal(encoded, &checkpoint) != nil || !validSemanticCheckpoint(checkpoint) {
		return ErrCorrupt
	}
	if checkpoint.After == sourceID {
		checkpoint.After = ""
		checkpoint.Complete = false
		checkpoint.Indexed = 0
		checkpoint.Skipped = 0
		updated, err := json.Marshal(checkpoint)
		if err != nil {
			return ErrCorrupt
		}
		defer clear(updated)
		return tx.putIndexValueLocked(semanticMetaNamespace, semanticCheckpointID, updated)
	}
	return nil
}
