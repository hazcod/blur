package durable

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

// Replication support. Both listings are bounded and use only exact scans over
// existing encrypted records; neither adds a second copy of captured text.

const (
	tombstoneNS         = "tombstones"
	tombstoneLogID      = "log"
	tombstoneEntryBytes = 8 + 1 + 32
	// MaxTombstones bounds the persisted deletion log. When full, the oldest
	// entries are dropped; a replica that never saw them can still resurrect
	// those sources, which is the documented phase-one limit.
	MaxTombstones = 16384
	// AllSourcesTombstone is the SourceID of a "delete everything captured
	// before At" tombstone.
	AllSourcesTombstone = "*"

	exportScanRows   = 512
	exportFetchBatch = 32
)

// Tombstone records a user deletion so other devices can replay it.
type Tombstone struct {
	SourceID string
	At       time.Time
}

// RecordTombstone adds a deletion to the log. A source ID already present is
// replaced only by a later deletion, which keeps relayed deletions (same time)
// from echoing between devices forever.
func (s *Store) RecordTombstone(ctx context.Context, t Tombstone) error {
	if ctx == nil || t.At.IsZero() || !validCaptureTime(t.At) || !validTombstoneID(t.SourceID) {
		return &storage.StoreError{Code: storage.Invalid}
	}
	return s.Update(ctx, func(tx *Tx) error {
		encoded, found, err := tx.readIndexValue(tombstoneNS, tombstoneLogID, MaxTombstones*tombstoneEntryBytes+1)
		if err != nil {
			return err
		}
		var log []Tombstone
		if found {
			log, err = decodeTombstones(encoded)
			clear(encoded)
			if err != nil {
				return err
			}
		}
		for i, existing := range log {
			if existing.SourceID != t.SourceID {
				continue
			}
			if t.SourceID == AllSourcesTombstone && !existing.At.Equal(t.At) {
				continue
			}
			if !t.At.After(existing.At) {
				return nil
			}
			log = append(log[:i], log[i+1:]...) // later deletion of the same source replaces it
			break
		}
		log = append(log, Tombstone{SourceID: t.SourceID, At: t.At.UTC()})
		sortTombstones(log)
		if len(log) > MaxTombstones {
			log = log[len(log)-MaxTombstones:]
		}
		return tx.PutIndexRecord(tombstoneNS, tombstoneLogID, encodeTombstones(log))
	})
}

// Tombstones returns the deletion log ordered by time, then source ID.
func (s *Store) Tombstones(ctx context.Context) ([]Tombstone, error) {
	encoded, found, err := s.readIndexValue(ctx, tombstoneNS, tombstoneLogID, MaxTombstones*tombstoneEntryBytes+1)
	if err != nil || !found {
		return nil, err
	}
	defer clear(encoded)
	return decodeTombstones(encoded)
}

// TombstoneCovers reports whether log deletes a source with the given ID and
// authenticated capture time. A capture after the deletion is a new capture.
func TombstoneCovers(log []Tombstone, sourceID string, capturedAt time.Time) bool {
	for _, t := range log {
		if t.SourceID == sourceID && !capturedAt.After(t.At) || t.SourceID == AllSourcesTombstone && capturedAt.Before(t.At) {
			return true
		}
	}
	return false
}

func validTombstoneID(id string) bool { return id == AllSourcesTombstone || validHash(id) }

func sortTombstones(log []Tombstone) {
	sort.SliceStable(log, func(i, j int) bool {
		if !log[i].At.Equal(log[j].At) {
			return log[i].At.Before(log[j].At)
		}
		return log[i].SourceID < log[j].SourceID
	})
}

func encodeTombstones(log []Tombstone) []byte {
	out := make([]byte, 0, len(log)*tombstoneEntryBytes)
	for _, t := range log {
		var at [8]byte
		binary.BigEndian.PutUint64(at[:], uint64(t.At.UTC().UnixNano()))
		out = append(out, at[:]...)
		if t.SourceID == AllSourcesTombstone {
			out = append(out, 0)
			continue
		}
		id, _ := hex.DecodeString(t.SourceID)
		out = append(out, 32)
		out = append(out, id...)
	}
	return out
}

func decodeTombstones(encoded []byte) ([]Tombstone, error) {
	var log []Tombstone
	for len(encoded) > 0 {
		if len(encoded) < 9 || len(log) >= MaxTombstones {
			return nil, ErrCorrupt
		}
		at := time.Unix(0, int64(binary.BigEndian.Uint64(encoded[:8]))).UTC()
		idLen := int(encoded[8])
		encoded = encoded[9:]
		t := Tombstone{At: at, SourceID: AllSourcesTombstone}
		switch idLen {
		case 0:
		case 32:
			if len(encoded) < 32 {
				return nil, ErrCorrupt
			}
			t.SourceID = hex.EncodeToString(encoded[:32])
			encoded = encoded[32:]
		default:
			return nil, ErrCorrupt
		}
		if !validCaptureTime(at) {
			return nil, ErrCorrupt
		}
		log = append(log, t)
	}
	return log, nil
}

// ExportedObservation is one stored observation in its authenticated record
// encoding together with its authenticated capture time.
type ExportedObservation struct {
	CapturedAt time.Time
	Record     []byte
}

type exportCandidate struct {
	at        time.Time
	recordKey []byte
}

// ExportObservations returns observations captured strictly after `after`, in
// (capture time, keyed record order). It returns at most maxCount records and
// about maxBytes of record bytes, never splitting a group that shares one
// capture time unless the group alone exceeds the budget (so a time watermark
// is always a safe resume point). more reports remaining observations.
func (s *Store) ExportObservations(ctx context.Context, after time.Time, maxCount, maxBytes int) (items []ExportedObservation, more bool, err error) {
	if ctx == nil || maxCount < 1 || maxCount > 1<<16 || maxBytes < 1 || maxBytes > 64<<20 {
		return nil, false, &storage.StoreError{Code: storage.Invalid}
	}
	var candidates []exportCandidate
	defer func() {
		for _, c := range candidates {
			clear(c.recordKey)
		}
	}()
	limit := maxCount + 1
	trim := func() {
		sort.Slice(candidates, func(i, j int) bool {
			if !candidates[i].at.Equal(candidates[j].at) {
				return candidates[i].at.Before(candidates[j].at)
			}
			return bytes.Compare(candidates[i].recordKey, candidates[j].recordKey) < 0
		})
		if len(candidates) > limit {
			for _, c := range candidates[limit:] {
				clear(c.recordKey)
			}
			candidates = candidates[:limit]
		}
	}
	var cursor []byte
	defer func() { clear(cursor) }()
	for {
		done := false
		err = s.view(ctx, func(reader bolt.Reader) error {
			entries, hasMore, err := reader.Scan(observationOrderPrefix, cursor, exportScanRows, bolt.MaxTransactionBytes-bolt.MaxKeyBytes)
			if err != nil {
				return mapStorePhysicalError(err)
			}
			defer clearBoltEntries(entries)
			if len(entries) == 0 {
				if hasMore {
					return ErrCorrupt
				}
				done = true
				return nil
			}
			cursor = append(cursor[:0], entries[len(entries)-1].Key...)
			for _, entry := range entries {
				_, at, recordKey, err := s.decodeSizeEntry(entry.Key, entry.Value)
				if err != nil {
					return ErrCorrupt
				}
				if at.IsZero() || !at.After(after) {
					clear(recordKey)
					continue
				}
				candidates = append(candidates, exportCandidate{at: at, recordKey: recordKey})
			}
			done = !hasMore
			return nil
		})
		if err != nil {
			return nil, false, err
		}
		if len(candidates) >= 2*limit {
			trim()
		}
		if done {
			break
		}
	}
	trim()
	more = len(candidates) > maxCount
	if more {
		candidates = candidates[:maxCount+1]
	}
	used := 0
	for start := 0; start < len(candidates) && len(items) < maxCount; start += exportFetchBatch {
		end := min(start+exportFetchBatch, len(candidates))
		stop := false
		err = s.view(ctx, func(reader bolt.Reader) error {
			for _, c := range candidates[start:end] {
				if len(items) >= maxCount {
					stop = true
					return nil
				}
				envelope, found, err := reader.Get(c.recordKey)
				if err != nil {
					return mapStorePhysicalError(err)
				}
				if !found {
					return ErrCorrupt
				}
				plain, err := s.open(c.recordKey, "observations", envelope)
				clear(envelope)
				if err != nil {
					return ErrCorrupt
				}
				if _, err := capture.RestoreAuthenticatedObservation(plain); err != nil {
					clear(plain)
					return ErrCorrupt
				}
				if len(items) > 0 && used+len(plain) > maxBytes {
					clear(plain)
					stop = true
					return nil
				}
				used += len(plain)
				items = append(items, ExportedObservation{CapturedAt: c.at, Record: plain})
			}
			return nil
		})
		if err != nil {
			for _, item := range items {
				clear(item.Record)
			}
			return nil, false, err
		}
		if stop {
			break
		}
	}
	// A time watermark must not split a group with identical capture times.
	if len(items) < len(candidates) {
		more = true
		if n := len(items); n > 1 && items[n-1].CapturedAt.Equal(candidates[n].at) {
			cut := n
			for cut > 0 && items[cut-1].CapturedAt.Equal(candidates[n].at) {
				cut--
			}
			if cut > 0 {
				for _, item := range items[cut:] {
					clear(item.Record)
				}
				items = items[:cut]
			}
		}
	}
	return items, more, nil
}

// Import origin tracking. An observation that arrived from another device's
// segment is remembered by content ID so this device never re-exports it
// (the origin device already holds it). IDs live in 256 encrypted shard
// records keyed by the first ID byte; each shard keeps the newest
// maxImportedPerShard IDs. Forgetting an old ID only risks a harmless
// duplicate export, because importers dedupe by content ID.
const (
	importedNS          = "imported"
	maxImportedPerShard = 8192
)

func importedShard(id string) string { return id[:2] }

func decodeImportedShard(encoded []byte) ([][32]byte, error) {
	if len(encoded)%32 != 0 || len(encoded)/32 > maxImportedPerShard {
		return nil, ErrCorrupt
	}
	ids := make([][32]byte, len(encoded)/32)
	for i := range ids {
		copy(ids[i][:], encoded[i*32:])
	}
	return ids, nil
}

// MarkImported remembers content IDs that were imported from another device.
func (s *Store) MarkImported(ctx context.Context, ids []string) error {
	if ctx == nil {
		return &storage.StoreError{Code: storage.Invalid}
	}
	shards := map[string][][32]byte{}
	for _, id := range ids {
		if !validHash(id) {
			return &storage.StoreError{Code: storage.Invalid}
		}
		raw, _ := hex.DecodeString(id)
		var key [32]byte
		copy(key[:], raw)
		shards[importedShard(id)] = append(shards[importedShard(id)], key)
	}
	if len(shards) == 0 {
		return nil
	}
	return s.Update(ctx, func(tx *Tx) error {
		for shard, add := range shards {
			encoded, _, err := tx.readIndexValue(importedNS, shard, maxImportedPerShard*32)
			if err != nil {
				return err
			}
			existing, err := decodeImportedShard(encoded)
			clear(encoded)
			if err != nil {
				return err
			}
			seen := make(map[[32]byte]bool, len(existing))
			for _, id := range existing {
				seen[id] = true
			}
			for _, id := range add {
				if !seen[id] {
					seen[id] = true
					existing = append(existing, id)
				}
			}
			if len(existing) > maxImportedPerShard {
				existing = existing[len(existing)-maxImportedPerShard:]
			}
			out := make([]byte, 0, len(existing)*32)
			for _, id := range existing {
				out = append(out, id[:]...)
			}
			err = tx.PutIndexRecord(importedNS, shard, out)
			clear(out)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// ImportedSet reports which of ids were recorded by MarkImported.
func (s *Store) ImportedSet(ctx context.Context, ids []string) (map[string]bool, error) {
	if ctx == nil {
		return nil, &storage.StoreError{Code: storage.Invalid}
	}
	out := map[string]bool{}
	byShard := map[string][]string{}
	for _, id := range ids {
		if !validHash(id) {
			return nil, &storage.StoreError{Code: storage.Invalid}
		}
		byShard[importedShard(id)] = append(byShard[importedShard(id)], id)
	}
	for shard, want := range byShard {
		encoded, found, err := s.readIndexValue(ctx, importedNS, shard, maxImportedPerShard*32)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		stored, err := decodeImportedShard(encoded)
		clear(encoded)
		if err != nil {
			return nil, err
		}
		set := make(map[string]bool, len(stored))
		for _, id := range stored {
			set[hex.EncodeToString(id[:])] = true
		}
		for _, id := range want {
			if set[id] {
				out[id] = true
			}
		}
	}
	return out, nil
}

// ClearImported forgets all import-origin marks (used by delete-all).
func (s *Store) ClearImported(ctx context.Context) error {
	return s.Update(ctx, func(tx *Tx) error {
		for i := 0; i < 256; i++ {
			shard := fmt.Sprintf("%02x", i)
			encoded, found, err := tx.readIndexValue(importedNS, shard, maxImportedPerShard*32)
			clear(encoded)
			if err != nil {
				return err
			}
			if !found {
				continue
			}
			if err := tx.deleteKnowledgeRecord(importedNS, shard); err != nil {
				return err
			}
		}
		return nil
	})
}
