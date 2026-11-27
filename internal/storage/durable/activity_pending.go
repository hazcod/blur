package durable

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const (
	activityPendingNamespace = "activity-pending-v1"
	activityPendingSources   = "activity-pending-src-v1"
	activityPendingNodes     = "activity-pending-links-v1"
)

type activityPendingDirectory struct {
	First string `json:"first,omitempty"`
	Last  string `json:"last,omitempty"`
}

type activityPendingRecord struct {
	Segment activity.Segment `json:"segment"`
	Before  string           `json:"before,omitempty"`
	After   string           `json:"after,omitempty"`
}

func (tx *Tx) activityPendingDirectory() (activityPendingDirectory, error) {
	return tx.store.activityPendingDirectoryReader(tx.writer)
}

func (s *Store) activityPendingDirectoryReader(reader bolt.Reader) (activityPendingDirectory, error) {
	encoded, found, err := s.readIndexValueReader(reader, activityPendingNamespace, "directory", 1024)
	if err != nil || !found {
		return activityPendingDirectory{}, err
	}
	defer clear(encoded)
	var directory activityPendingDirectory
	if json.Unmarshal(encoded, &directory) != nil || (directory.First == "") != (directory.Last == "") || directory.First != "" && !validOpaqueID(directory.First) || directory.Last != "" && !validOpaqueID(directory.Last) {
		return activityPendingDirectory{}, ErrCorrupt
	}
	return directory, nil
}

func (tx *Tx) activityPendingRecord(id string) (activityPendingRecord, bool, error) {
	return tx.store.activityPendingRecordReader(tx.writer, id)
}

func (s *Store) activityPendingRecordReader(reader bolt.Reader, id string) (activityPendingRecord, bool, error) {
	encoded, found, err := s.readIndexValueReader(reader, activityPendingNamespace, "segment:"+id, maxActivityRecordBytes+1024)
	if err != nil || !found {
		return activityPendingRecord{}, found, err
	}
	defer clear(encoded)
	var record activityPendingRecord
	if json.Unmarshal(encoded, &record) != nil || record.Segment.Validate() != nil || record.Segment.ID != id || record.Before != "" && !validOpaqueID(record.Before) || record.After != "" && !validOpaqueID(record.After) || record.Before == id || record.After == id {
		return activityPendingRecord{}, false, ErrCorrupt
	}
	return record, true, nil
}

// StageActivitySegment acknowledges only encrypted durable acceptance. Source
// membership and deletion cutoffs are checked in the same transaction.
func (s *Store) StageActivitySegment(ctx context.Context, segment activity.Segment) error {
	if ctx == nil || segment.Validate() != nil {
		return &storage.StoreError{Code: storage.Invalid}
	}
	return s.Update(ctx, func(tx *Tx) error {
		if err := tx.validateActivitySources(segment, true); err != nil {
			return err
		}
		if prior, found, err := tx.activityPendingRecord(segment.ID); err != nil || found {
			if err != nil {
				return err
			}
			before, after := mustJSON(prior.Segment), mustJSON(segment)
			defer clear(before)
			defer clear(after)
			if !bytes.Equal(before, after) {
				return &storage.StoreError{Code: storage.Invalid}
			}
			return nil
		}
		var stored activity.Segment
		if found, err := s.readActivitySegmentReader(tx.writer, segment.ID, &stored); err != nil || found {
			return err
		}
		directory, err := tx.activityPendingDirectory()
		if err != nil {
			return err
		}
		if directory.Last != "" {
			tail, found, err := tx.activityPendingRecord(directory.Last)
			if err != nil || !found || tail.After != "" {
				if err != nil {
					return err
				}
				return ErrCorrupt
			}
			tail.After = segment.ID
			if err := tx.PutIndexRecord(activityPendingNamespace, "segment:"+directory.Last, mustJSON(tail)); err != nil {
				return err
			}
		} else {
			directory.First = segment.ID
		}
		for _, sourceID := range segment.SourceIDs {
			if err := tx.trackActivityLink(activityPendingSources, activityPendingNodes, sourceID, segment.ID); err != nil {
				return err
			}
		}
		if err := tx.PutIndexRecord(activityPendingNamespace, "segment:"+segment.ID, mustJSON(activityPendingRecord{Segment: segment, Before: directory.Last})); err != nil {
			return err
		}
		directory.Last = segment.ID
		return tx.PutIndexRecord(activityPendingNamespace, "directory", mustJSON(directory))
	})
}

func (tx *Tx) validateActivitySources(segment activity.Segment, strict bool) error {
	for _, sourceID := range segment.SourceIDs {
		if !validHash(sourceID) {
			if strict {
				return &storage.StoreError{Code: storage.Invalid}
			}
			continue // Preserve legacy imported opaque source references.
		}
		_, present, err := tx.store.sourceAdmissionReader(tx.writer, sourceID)
		if err != nil {
			return err
		}
		current, err := tx.store.isCurrentNoteSourceReader(tx.writer, sourceID)
		if err != nil {
			return err
		}
		deleted, err := tx.store.deletionCoversReader(tx.writer, sourceID, segment.WallStart)
		if err != nil {
			return err
		}
		if !present || !current || deleted {
			return &storage.StoreError{Code: storage.Invalid}
		}
	}
	return nil
}

func (s *Store) PeekPendingActivity(ctx context.Context) (activity.Segment, bool, error) {
	if ctx == nil {
		return activity.Segment{}, false, &storage.StoreError{Code: storage.Invalid}
	}
	var segment activity.Segment
	found := false
	err := s.view(ctx, func(reader bolt.Reader) error {
		directory, err := s.activityPendingDirectoryReader(reader)
		if err != nil || directory.First == "" {
			return err
		}
		record, exists, err := s.activityPendingRecordReader(reader, directory.First)
		if err != nil || !exists || record.Before != "" {
			if err != nil {
				return err
			}
			return ErrCorrupt
		}
		segment, found = record.Segment, true
		return nil
	})
	return segment, found, err
}

func (s *Store) DiscardPendingActivity(ctx context.Context, id string) error {
	if ctx == nil || !validOpaqueID(id) {
		return &storage.StoreError{Code: storage.Invalid}
	}
	// Normal final writes already removed the row atomically. Avoid acquiring
	// the writer and publication locks again just to acknowledge that no-op.
	found := false
	if err := s.view(ctx, func(reader bolt.Reader) error {
		var err error
		_, found, err = s.activityPendingRecordReader(reader, id)
		return err
	}); err != nil || !found {
		return err
	}
	return s.Update(ctx, func(tx *Tx) error { return tx.discardPendingActivity(id) })
}

func (tx *Tx) discardPendingActivity(id string) error {
	record, found, err := tx.activityPendingRecord(id)
	if err != nil || !found {
		return err
	}
	directory, err := tx.activityPendingDirectory()
	if err != nil {
		return err
	}
	if record.Before == "" {
		if directory.First != id {
			return ErrCorrupt
		}
		directory.First = record.After
	} else {
		prior, found, err := tx.activityPendingRecord(record.Before)
		if err != nil || !found || prior.After != id {
			if err != nil {
				return err
			}
			return ErrCorrupt
		}
		prior.After = record.After
		if err := tx.PutIndexRecord(activityPendingNamespace, "segment:"+record.Before, mustJSON(prior)); err != nil {
			return err
		}
	}
	if record.After == "" {
		if directory.Last != id {
			return ErrCorrupt
		}
		directory.Last = record.Before
	} else {
		next, found, err := tx.activityPendingRecord(record.After)
		if err != nil || !found || next.Before != id {
			if err != nil {
				return err
			}
			return ErrCorrupt
		}
		next.Before = record.Before
		if err := tx.PutIndexRecord(activityPendingNamespace, "segment:"+record.After, mustJSON(next)); err != nil {
			return err
		}
	}
	for _, sourceID := range record.Segment.SourceIDs {
		if err := tx.untrackActivityLink(activityPendingSources, activityPendingNodes, sourceID, id); err != nil {
			return err
		}
	}
	if err := tx.deleteKnowledgeRecord(activityPendingNamespace, "segment:"+id); err != nil {
		return err
	}
	return tx.PutIndexRecord(activityPendingNamespace, "directory", mustJSON(directory))
}

func (tx *Tx) removePendingActivitySourceBatch(sourceID string, limit int) (bool, error) {
	list, err := tx.activitySourceList(activityPendingSources, sourceID)
	if err != nil || list.Count == 0 {
		return true, err
	}
	for removed := 0; removed < limit && list.First != ""; removed++ {
		if err := tx.discardPendingActivity(list.First); err != nil {
			return false, err
		}
		list, err = tx.activitySourceList(activityPendingSources, sourceID)
		if err != nil {
			return false, err
		}
	}
	// Yield after touching pending work; the next bounded step handles any
	// final published intervals once this source's outbox has drained.
	return false, nil
}
