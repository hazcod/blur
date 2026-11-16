package durable

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"sort"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const (
	observationSightingNS = "observation-visits-v1"
	maxSightingTimes      = 65536
	maxSightingHourBytes  = 2 << 20
	maxSightingMonthBytes = 32 << 10
)

// Each source/hour is linked into its source's erasure chain. The timeline
// contains only the latest visit in that hour; exact times preserve arbitrary
// sub-hour windows without copying or re-indexing the primary text.
type observationSightingHour struct {
	Page     string  `json:"page"`
	Previous string  `json:"previous,omitempty"`
	Times    []int64 `json:"times"`
}

func sightingHourKey(id, hour string) string { return id + ":" + hour }
func sightingMonthKey(id string, at time.Time) string {
	return id + ":month:" + at.UTC().Format("200601")
}

func decodeSightingHour(encoded []byte, hour string) (observationSightingHour, error) {
	var node observationSightingHour
	if json.Unmarshal(encoded, &node) != nil || len(node.Times) == 0 || len(node.Times) > maxSightingTimes {
		return node, ErrCorrupt
	}
	pageHour, _, err := parseObservationTimelinePageID(node.Page)
	if err != nil || pageHour != hour || node.Previous != "" && (!validChronicleHourID(node.Previous) || node.Previous == hour) {
		return node, ErrCorrupt
	}
	for i, n := range node.Times {
		at := time.Unix(0, n).UTC()
		if !validCaptureTime(at) || observationTimelineHourID(at) != hour || i > 0 && node.Times[i-1] >= n {
			return node, ErrCorrupt
		}
	}
	return node, nil
}

func decodeSightingMonth(encoded []byte, month string) ([]int64, error) {
	var latest []int64
	if json.Unmarshal(encoded, &latest) != nil || len(latest) == 0 || len(latest) > 31*24 {
		return nil, ErrCorrupt
	}
	for i, n := range latest {
		at := time.Unix(0, n).UTC()
		if !validCaptureTime(at) || at.Format("200601") != month || i > 0 && time.Unix(0, latest[i-1]).UTC().Truncate(time.Hour).Equal(at.Truncate(time.Hour)) || i > 0 && latest[i-1] >= n {
			return nil, ErrCorrupt
		}
	}
	return latest, nil
}

// RecordObservationSighting admits metadata only while its immutable source
// still exists, in the same transaction as the visit indexes. A late capture
// cannot recreate a deleted source through this API.
func (s *Store) RecordObservationSighting(ctx context.Context, observation capture.SanitizedObservation, at time.Time) error {
	if observation.Validate() != nil || !validCaptureTime(at) {
		return &storage.StoreError{Code: storage.Invalid}
	}
	return s.Update(ctx, func(tx *Tx) error { return tx.recordObservationSighting(observation, at) })
}

func (tx *Tx) recordObservationSighting(observation capture.SanitizedObservation, at time.Time) error {
	if tx == nil {
		return &storage.StoreError{Code: storage.Closed}
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if err := tx.checkLocked(); err != nil {
		return tx.failLocked(err)
	}
	if observation.Validate() != nil || !validCaptureTime(at) {
		return tx.failLocked(&storage.StoreError{Code: storage.Invalid})
	}
	if err := tx.checkSourceAdmissionLocked(observation.ContentID(), at); err != nil {
		return tx.failLocked(err)
	}
	s := tx.store
	id := observation.ContentID()
	key := s.logicalKey(groupRecord, kindObservation, "", id)
	found, err := tx.writer.Has(key)
	clear(key)
	if err != nil {
		return mapStorePhysicalError(err)
	}
	if !found {
		return &storage.StoreError{Code: storage.Invalid}
	}
	first, err := s.observationCapturedAtReader(tx.writer, id)
	if err != nil {
		return err
	}
	if at.Equal(first) {
		return nil
	}
	hour := observationTimelineHourID(at)
	encoded, found, err := tx.readIndexValueLocked(observationSightingNS, sightingHourKey(id, hour), maxSightingHourBytes)
	if err != nil {
		return err
	}
	var node observationSightingHour
	newHour := !found
	if found {
		node, err = decodeSightingHour(encoded, hour)
		clear(encoded)
		if err != nil {
			return err
		}
	} else {
		head, exists, err := tx.readIndexValueLocked(observationSightingNS, id, 64)
		if err != nil {
			return err
		}
		if exists {
			node.Previous = string(head)
			clear(head)
			if !validChronicleHourID(node.Previous) || node.Previous == hour {
				return ErrCorrupt
			}
		}
		if hour == observationTimelineHourID(first) {
			ref, exists, err := tx.readIndexValueLocked(observationTimelineSourceNS, id, 64)
			if err != nil {
				return err
			}
			if !exists {
				return ErrCorrupt
			}
			node.Page = string(ref)
			clear(ref)
			refHour, _, err := parseObservationTimelinePageID(node.Page)
			if err != nil || refHour != hour {
				return ErrCorrupt
			}
			node.Times = []int64{first.UnixNano()}
		} else {
			node.Page, err = tx.appendObservationTimelineLocked(observation, at)
			if err != nil {
				return err
			}
		}
	}
	n := at.UnixNano()
	position := sort.Search(len(node.Times), func(i int) bool { return node.Times[i] >= n })
	if position < len(node.Times) && node.Times[position] == n {
		return nil
	}
	if len(node.Times) == maxSightingTimes {
		return &storage.StoreError{Code: storage.Incomplete}
	}
	node.Times = append(node.Times, 0)
	copy(node.Times[position+1:], node.Times[position:])
	node.Times[position] = n
	latest := time.Unix(0, node.Times[len(node.Times)-1]).UTC()
	if err := tx.changeTimelineVisitLocked(id, node.Page, latest, false); err != nil {
		return err
	}
	encoded, err = json.Marshal(node)
	if err != nil {
		return ErrCorrupt
	}
	defer clear(encoded)
	if err := tx.putIndexValueLocked(observationSightingNS, sightingHourKey(id, hour), encoded); err != nil {
		return err
	}
	if err := tx.changeSightingMonthLocked(id, latest, false); err != nil {
		return err
	}
	if newHour {
		return tx.putIndexValueLocked(observationSightingNS, id, []byte(hour))
	}
	return nil
}

func (tx *Tx) changeSightingMonthLocked(id string, at time.Time, remove bool) error {
	key := sightingMonthKey(id, at)
	encoded, found, err := tx.readIndexValueLocked(observationSightingNS, key, maxSightingMonthBytes)
	if err != nil {
		return err
	}
	var latest []int64
	if found {
		latest, err = decodeSightingMonth(encoded, at.UTC().Format("200601"))
		clear(encoded)
		if err != nil {
			return err
		}
	}
	hour := at.UTC().Truncate(time.Hour)
	position := sort.Search(len(latest), func(i int) bool { return !time.Unix(0, latest[i]).UTC().Truncate(time.Hour).Before(hour) })
	exists := position < len(latest) && time.Unix(0, latest[position]).UTC().Truncate(time.Hour).Equal(hour)
	if remove {
		if !exists {
			return ErrCorrupt
		}
		latest = append(latest[:position], latest[position+1:]...)
		if len(latest) == 0 {
			return tx.deleteIndexValueLocked(observationSightingNS, key)
		}
	} else if exists {
		latest[position] = at.UnixNano()
	} else {
		latest = append(latest, 0)
		copy(latest[position+1:], latest[position:])
		latest[position] = at.UnixNano()
	}
	encoded, err = json.Marshal(latest)
	if err != nil {
		return ErrCorrupt
	}
	defer clear(encoded)
	return tx.putIndexValueLocked(observationSightingNS, key, encoded)
}

func (tx *Tx) deleteIndexValueLocked(namespace, id string) error {
	key := tx.store.logicalKey(groupRecord, kindIndex, "index:"+namespace, id)
	defer clear(key)
	return tx.deleteEntryLocked(key)
}

// changeTimelineVisitLocked changes one authenticated source/hour row. The
// immutable canonical reverse reference is preserved until primary deletion.
func (tx *Tx) changeTimelineVisitLocked(id, pageID string, at time.Time, remove bool) error {
	hour, _, err := parseObservationTimelinePageID(pageID)
	if err != nil {
		return err
	}
	encoded, found, err := tx.readIndexValueLocked(observationTimelineNS, pageID, maxObservationTimelinePage)
	if err != nil {
		return err
	}
	if !found {
		return ErrCorrupt
	}
	entries, err := decodeObservationTimelinePage(encoded, hour)
	clear(encoded)
	if err != nil {
		return err
	}
	index := -1
	for i, entry := range entries {
		if entry.id == id {
			index = i
			break
		}
	}
	if index < 0 {
		return ErrCorrupt
	}
	if remove {
		entries = append(entries[:index], entries[index+1:]...)
	} else {
		if observationTimelineHourID(at) != hour {
			return ErrCorrupt
		}
		entries[index].at = at
	}
	encoded = encodeObservationTimelinePage(entries)
	defer clear(encoded)
	if err := tx.putIndexValueLocked(observationTimelineNS, pageID, encoded); err != nil {
		return err
	}
	if !remove {
		return nil
	}
	metaBytes, found, err := tx.readIndexValueLocked(observationTimelineNS, hour, 9)
	if err != nil {
		return err
	}
	if !found {
		return ErrCorrupt
	}
	meta, err := decodeObservationTimelineMeta(metaBytes)
	clear(metaBytes)
	if err != nil || meta.count < 1 {
		return ErrCorrupt
	}
	meta.count--
	return tx.putIndexValueLocked(observationTimelineNS, hour, encodeObservationTimelineMeta(meta))
}

// eraseObservationSighting removes at most one hour, independently of the
// source's age. The caller commits the erasure journal and this step together.
func (tx *Tx) eraseObservationSighting(id string) (bool, error) {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	head, found, err := tx.readIndexValueLocked(observationSightingNS, id, 64)
	if err != nil || !found {
		return !found, err
	}
	hour := string(head)
	clear(head)
	if !validChronicleHourID(hour) {
		return false, ErrCorrupt
	}
	encoded, found, err := tx.readIndexValueLocked(observationSightingNS, sightingHourKey(id, hour), maxSightingHourBytes)
	if err != nil {
		return false, err
	}
	if !found {
		return false, ErrCorrupt
	}
	node, err := decodeSightingHour(encoded, hour)
	clear(encoded)
	if err != nil {
		return false, err
	}
	first, err := tx.store.observationCapturedAtReader(tx.writer, id)
	if err != nil {
		return false, err
	}
	if err := tx.changeTimelineVisitLocked(id, node.Page, first, hour != observationTimelineHourID(first)); err != nil {
		return false, err
	}
	if err := tx.changeSightingMonthLocked(id, time.Unix(0, node.Times[len(node.Times)-1]).UTC(), true); err != nil {
		return false, err
	}
	if err := tx.deleteIndexValueLocked(observationSightingNS, sightingHourKey(id, hour)); err != nil {
		return false, err
	}
	if node.Previous == "" {
		err = tx.deleteIndexValueLocked(observationSightingNS, id)
	} else {
		err = tx.putIndexValueLocked(observationSightingNS, id, []byte(node.Previous))
	}
	return false, err
}

func (s *Store) sightingAtBeforeReader(reader bolt.Reader, id string, at, end time.Time) (time.Time, bool, error) {
	hour := observationTimelineHourID(at)
	encoded, found, err := s.readIndexValueReader(reader, observationSightingNS, sightingHourKey(id, hour), maxSightingHourBytes)
	if err != nil {
		return time.Time{}, false, err
	}
	if !found {
		return at, at.Before(end) && s.IsObservationTimeKnown(at), nil
	}
	node, err := decodeSightingHour(encoded, hour)
	clear(encoded)
	if err != nil || node.Times[len(node.Times)-1] != at.UnixNano() {
		return time.Time{}, false, ErrCorrupt
	}
	index := sort.Search(len(node.Times), func(i int) bool { return node.Times[i] >= end.UnixNano() })
	for index > 0 && !s.IsObservationTimeKnown(time.Unix(0, node.Times[index-1]).UTC()) {
		index--
	}
	if index == 0 {
		return time.Time{}, false, nil
	}
	return time.Unix(0, node.Times[index-1]).UTC(), true, nil
}

func (s *Store) verifySightingReader(reader bolt.Reader, id string, at, first time.Time) error {
	if at.Equal(first) {
		return nil
	}
	hour := observationTimelineHourID(at)
	encoded, found, err := s.readIndexValueReader(reader, observationSightingNS, sightingHourKey(id, hour), maxSightingHourBytes)
	if err != nil {
		return err
	}
	if !found {
		return ErrCorrupt
	}
	node, err := decodeSightingHour(encoded, hour)
	clear(encoded)
	if err != nil {
		return err
	}
	index := sort.Search(len(node.Times), func(i int) bool { return node.Times[i] >= at.UnixNano() })
	if index == len(node.Times) || node.Times[index] != at.UnixNano() {
		return ErrCorrupt
	}
	return nil
}

type sightingReader func(namespace, id string, maxBytes int) ([]byte, bool, error)

// ObservationSightingAt returns the latest exact visit in the requested
// interval. Source/month metadata keeps this lookup independent of the age of
// the source and the number of historical visits.
func (s *Store) ObservationSightingAt(ctx context.Context, id string, start, end time.Time) (time.Time, bool, error) {
	if ctx == nil || !validHash(id) || !storage.ValidObservationWindow(storage.ObservationWindowRequest{Start: start, End: end, Limit: 1, MaxBytes: storage.MaxObservationPageBytes}) {
		return time.Time{}, false, &storage.StoreError{Code: storage.Invalid}
	}
	var at time.Time
	var found bool
	err := s.view(ctx, func(reader bolt.Reader) error {
		key := s.logicalKey(groupRecord, kindObservation, "", id)
		exists, err := reader.Has(key)
		clear(key)
		if err != nil {
			return mapStorePhysicalError(err)
		}
		if !exists {
			return nil
		}
		first, err := s.observationCapturedAtReader(reader, id)
		if err != nil {
			return err
		}
		at, found, err = s.sightingInWindow(id, first, start, end, func(ns, key string, maxBytes int) ([]byte, bool, error) {
			return s.readIndexValueReader(reader, ns, key, maxBytes)
		})
		return err
	})
	if err != nil {
		return time.Time{}, false, err
	}
	return at, found, nil
}

func (s *Store) sightingInWindow(id string, first, start, end time.Time, read sightingReader) (time.Time, bool, error) {
	var best time.Time
	if s.IsObservationTimeKnown(first) && !first.Before(start) && first.Before(end) {
		best = first
	}
	start, end = start.UTC(), end.UTC()
	month := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC)
	for month.Before(end) {
		encoded, found, err := read(observationSightingNS, sightingMonthKey(id, month), maxSightingMonthBytes)
		if err != nil {
			return time.Time{}, false, err
		}
		if found {
			latest, err := decodeSightingMonth(encoded, month.Format("200601"))
			clear(encoded)
			if err != nil {
				return time.Time{}, false, err
			}
			for i := len(latest) - 1; i >= 0; i-- {
				at := time.Unix(0, latest[i]).UTC()
				if at.Before(start) || !best.IsZero() && !at.After(best) {
					break
				}
				if !at.Truncate(time.Hour).Before(end) {
					continue
				}
				if !at.Before(end) || !s.IsObservationTimeKnown(at) {
					encoded, found, err := read(observationSightingNS, sightingHourKey(id, observationTimelineHourID(at)), maxSightingHourBytes)
					if err != nil {
						return time.Time{}, false, err
					}
					if !found {
						return time.Time{}, false, ErrCorrupt
					}
					node, err := decodeSightingHour(encoded, observationTimelineHourID(at))
					clear(encoded)
					if err != nil || node.Times[len(node.Times)-1] != at.UnixNano() {
						return time.Time{}, false, ErrCorrupt
					}
					index := sort.Search(len(node.Times), func(i int) bool { return node.Times[i] >= end.UnixNano() })
					for index > 0 && !s.IsObservationTimeKnown(time.Unix(0, node.Times[index-1]).UTC()) {
						index--
					}
					if index == 0 {
						continue
					}
					at = time.Unix(0, node.Times[index-1]).UTC()
				}
				if !at.Before(start) && at.Before(end) && (best.IsZero() || at.After(best)) {
					best = at
					break
				}
			}
		}
		month = month.AddDate(0, 1, 0)
	}
	return best, !best.IsZero(), nil
}

func (s *Store) searchSightingReader(reader bolt.Reader, page *search.ExactPage, maxBytes int) sightingReader {
	return func(namespace, id string, limit int) ([]byte, bool, error) {
		if page.Nodes >= search.MaxIndexNodes {
			return nil, false, search.ErrIndexBudget
		}
		page.Nodes++
		key := s.logicalKey(groupRecord, kindIndex, "index:"+namespace, id)
		defer clear(key)
		encoded, found, used, blocked, err := readBoundedIndexValue(reader, key, maxBytes-page.Bytes)
		page.Bytes += used
		if err != nil {
			return nil, false, err
		}
		if blocked {
			return nil, false, search.ErrIndexBudget
		}
		if !found {
			return nil, false, nil
		}
		defer clear(encoded)
		if len(encoded) > limit+1024 {
			return nil, false, ErrCorrupt
		}
		plain, err := s.open(key, "index:"+namespace, encoded)
		if err != nil {
			return nil, false, ErrCorrupt
		}
		if len(plain) < 5 || plain[0] != valueVersion || int(binary.BigEndian.Uint32(plain[1:5])) != len(plain)-5 || len(plain)-5 > limit {
			clear(plain)
			return nil, false, ErrCorrupt
		}
		value := append([]byte(nil), plain[5:]...)
		clear(plain)
		return value, true, nil
	}
}
