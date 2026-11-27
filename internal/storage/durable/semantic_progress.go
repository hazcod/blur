package durable

import (
	"context"

	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/semantic"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const semanticProgressNamespace = "semantic-progress-v1"

// Progress has no semantic order entry. Neither reloads nor queries can observe
// its vectors until PutSemanticDocument atomically publishes a complete source.
func (s *Store) PutSemanticProgress(ctx context.Context, progress semantic.Progress) error {
	if ctx == nil || progress.Validate() != nil {
		return &storage.StoreError{Code: storage.Invalid}
	}
	encoded, err := semantic.EncodeProgress(progress)
	if err != nil {
		return &storage.StoreError{Code: storage.Invalid}
	}
	defer clear(encoded)
	sourceGone := false
	err = s.Update(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		valid, err := s.semanticProgressSourceReader(tx.writer, progress)
		if err != nil {
			return tx.failLocked(err)
		}
		if !valid {
			sourceGone = true
			return tx.failLocked(&storage.StoreError{Code: storage.Invalid})
		}
		return tx.putIndexValueLocked(semanticProgressNamespace, progress.Document.SourceID, encoded)
	})
	if sourceGone && err != nil {
		return semantic.ErrSourceGone
	}
	return err
}

func (s *Store) GetSemanticProgress(ctx context.Context, sourceID, modelKey string) (semantic.Progress, bool, error) {
	if ctx == nil || !validHash(sourceID) || !validHash(modelKey) {
		return semantic.Progress{}, false, &storage.StoreError{Code: storage.Invalid}
	}
	var progress semantic.Progress
	found := false
	err := s.view(ctx, func(reader bolt.Reader) error {
		encoded, exists, err := s.readIndexValueReader(reader, semanticProgressNamespace, sourceID, semantic.MaxProgressBytes)
		if err != nil || !exists {
			return err
		}
		defer clear(encoded)
		progress, err = semantic.DecodeProgress(encoded)
		if err != nil || progress.Document.SourceID != sourceID {
			return ErrCorrupt
		}
		if progress.Document.Model.Key() != modelKey {
			return nil
		}
		found, err = s.semanticProgressSourceReader(reader, progress)
		return err
	})
	if err != nil || !found {
		clearSemanticProgress(progress)
		return semantic.Progress{}, false, err
	}
	return progress, true, nil
}

func (s *Store) semanticProgressSourceReader(reader bolt.Reader, progress semantic.Progress) (bool, error) {
	document := progress.Document
	source, at, found, err := s.sourceEvidenceReader(reader, document.SourceID)
	if err != nil || !found {
		return false, err
	}
	current, err := s.isCurrentNoteSourceReader(reader, document.SourceID)
	if err != nil || !current {
		return false, err
	}
	if document.AppID != source.AppID() || document.AppID == appidentity.BundleID || document.Domain != source.Domain() || document.TextBytes != len(source.Text()) || !document.CapturedAt.Equal(at) {
		return false, &storage.StoreError{Code: storage.Invalid}
	}
	spans, err := semantic.SplitText(source.Text())
	if err != nil || len(document.Chunks) > len(spans) {
		return false, &storage.StoreError{Code: storage.Invalid}
	}
	for i, chunk := range document.Chunks {
		if chunk.Start != spans[i].Start || chunk.End != spans[i].End {
			return false, &storage.StoreError{Code: storage.Invalid}
		}
	}
	return true, nil
}

func clearSemanticProgress(progress semantic.Progress) {
	for _, chunk := range progress.Document.Chunks {
		clear(chunk.Vector)
	}
}

func (tx *Tx) removeSemanticProgressLocked(sourceID string) error {
	key := tx.store.logicalKey(groupRecord, kindIndex, "index:"+semanticProgressNamespace, sourceID)
	defer clear(key)
	found, err := tx.writer.Has(key)
	if err != nil {
		return mapStorePhysicalError(err)
	}
	if !found {
		return nil
	}
	return tx.deleteEntryLocked(key)
}
