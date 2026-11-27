package durable

import (
	"bytes"
	"context"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

// ReplicaExportPage is one bounded physical metadata scan. Next is an opaque
// authenticated-record order key, never a content timestamp. An empty Next
// finishes a sweep; the next sweep discovers inserts before an earlier cursor.
type ReplicaExportPage struct {
	Items   []ExportedObservation
	Next    []byte
	HasMore bool
	Rows    int
}

type replicaExportCandidate struct {
	orderKey, recordKey []byte
	atExport            ExportedObservation
	acked               bool
}

func (s *Store) ExportReplicaObservations(ctx context.Context, generation string, after []byte, maxCount, maxBytes int) (page ReplicaExportPage, err error) {
	if _, ok := replicaGeneration(generation); ctx == nil || !ok || maxCount < 1 || maxCount > 1<<16 || maxBytes < 1 || maxBytes > 64<<20 || len(after) != 0 && (len(after) != 34 || !bytes.HasPrefix(after, observationOrderPrefix)) {
		return page, &storage.StoreError{Code: storage.Invalid}
	}
	var candidates []replicaExportCandidate
	defer func() {
		for _, c := range candidates {
			clear(c.orderKey)
			clear(c.recordKey)
		}
	}()
	var physicalMore bool
	err = s.view(ctx, func(reader bolt.Reader) error {
		entries, more, err := reader.Scan(observationOrderPrefix, after, exportScanRows, bolt.MaxTransactionBytes-bolt.MaxKeyBytes)
		if err != nil {
			return mapStorePhysicalError(err)
		}
		defer clearBoltEntries(entries)
		physicalMore = more
		page.Rows = len(entries)
		if len(entries) == 0 && more {
			return ErrCorrupt
		}
		for _, entry := range entries {
			_, at, recordKey, err := s.decodeSizeEntry(entry.Key, entry.Value)
			if err != nil {
				return err
			}
			revision := replicaObservationRevision(entry.Value)
			acked, err := s.replicaReceiptReader(reader, generation, recordKey, revision)
			if err != nil {
				clear(recordKey)
				return err
			}
			candidates = append(candidates, replicaExportCandidate{orderKey: bytes.Clone(entry.Key), recordKey: recordKey, atExport: ExportedObservation{CapturedAt: at, Revision: revision}, acked: acked || at.IsZero()})
		}
		return nil
	})
	if err != nil {
		return ReplicaExportPage{}, err
	}
	used, stopped := 0, false
	for _, candidate := range candidates {
		if !candidate.acked {
			if len(page.Items) >= maxCount {
				stopped = true
				break
			}
			var plain []byte
			err = s.view(ctx, func(reader bolt.Reader) error {
				envelope, found, err := reader.Get(candidate.recordKey)
				defer clear(envelope)
				if err != nil {
					return mapStorePhysicalError(err)
				}
				if !found {
					return nil
				}
				plain, err = s.open(candidate.recordKey, "observations", envelope)
				if err != nil {
					return err
				}
				o, err := capture.RestoreAuthenticatedObservation(plain)
				if err != nil {
					return ErrCorrupt
				}
				expected := s.logicalKey(groupRecord, kindObservation, "", o.ContentID())
				defer clear(expected)
				if !bytes.Equal(expected, candidate.recordKey) {
					return ErrCorrupt
				}
				return nil
			})
			if err != nil {
				clear(plain)
				clearExportPage(&page)
				return ReplicaExportPage{}, err
			}
			if len(plain) > 0 {
				if len(page.Items) > 0 && used+len(plain) > maxBytes {
					clear(plain)
					stopped = true
					break
				}
				item := candidate.atExport
				item.Record = plain
				page.Items = append(page.Items, item)
				used += len(plain)
			}
		}
		page.Next = append(page.Next[:0], candidate.orderKey...)
	}
	page.HasMore = stopped || physicalMore
	if !page.HasMore {
		clear(page.Next)
		page.Next = nil
	}
	return page, nil
}

func clearExportPage(page *ReplicaExportPage) {
	for _, item := range page.Items {
		clear(item.Record)
	}
	clear(page.Next)
}
