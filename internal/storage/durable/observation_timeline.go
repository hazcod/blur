package durable

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const (
	observationTimelineNS       = "observation-time"
	observationTimelineSourceNS = "observation-time-src"
	observationTimelineMarkerID = "observation-timeline-v1"
	observationTimelinePageSize = 64
	// Scan one authenticated directory page per transaction. Sparse windows
	// skip hours in bounded groups so callers can continue fairly.
	maxObservationTimelineEmptyHoursPerQuantum = 64
	maxTimelineCounter                         = int64(^uint32(0)) // binary metadata representation
	observationTimelineEntryBytes              = 104               // content hash, nanoseconds, keyed app/domain digests
	maxObservationTimelinePage                 = 5 + observationTimelinePageSize*observationTimelineEntryBytes
)

type captureWindowCandidate struct {
	id                    string
	at                    time.Time
	appDigest, hostDigest [32]byte
}

type observationTimelineMeta struct{ pages, count int }

// observationTimelineScanCursor is an in-process continuation for one bounded
// timeline scan quantum. It is separate from the public cursor, which pages
// globally ranked observations.
type observationTimelineScanCursor struct {
	hour        time.Time
	lastHour    time.Time
	page        int
	pages       int
	expected    int
	rowsRead    int
	hourSeen    map[string]struct{}
	initialized bool
	done        bool
}

func newObservationTimelineScanCursor(start, end time.Time) observationTimelineScanCursor {
	return observationTimelineScanCursor{hour: end.Add(-time.Nanosecond).UTC().Truncate(time.Hour), lastHour: start.UTC().Truncate(time.Hour)}
}

// observationTimelineQuantum reads no more than one directory page in a
// physical read transaction. The caller carries the cursor and deduplication
// state across quanta before publishing any ranked result.
func (s *Store) observationTimelineQuantum(ctx context.Context, namespace string, cursor observationTimelineScanCursor) ([]captureWindowCandidate, observationTimelineScanCursor, error) {
	var entries []captureWindowCandidate
	err := s.viewWithReadLimit(ctx, bolt.MaxReadTransactionOps, func(reader bolt.Reader) error {
		for skipped := 0; skipped < maxObservationTimelineEmptyHoursPerQuantum; skipped++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			if cursor.done || cursor.hour.Before(cursor.lastHour) {
				cursor.done = true
				return nil
			}
			hourID := observationTimelineHourID(cursor.hour)
			if !cursor.initialized {
				encoded, found, err := s.readIndexValueReader(reader, namespace, hourID, 9)
				if err != nil {
					return err
				}
				if !found {
					cursor.hour = cursor.hour.Add(-time.Hour)
					continue
				}
				meta, err := decodeObservationTimelineMeta(encoded)
				clear(encoded)
				if err != nil {
					return err
				}
				cursor.pages, cursor.expected, cursor.page, cursor.rowsRead, cursor.initialized = meta.pages, meta.count, 0, 0, true
				cursor.hourSeen = make(map[string]struct{}, observationTimelinePageSize)
				if meta.count == 0 {
					if meta.pages != 1 {
						return ErrCorrupt
					}
					cursor.hour = cursor.hour.Add(-time.Hour)
					cursor.initialized = false
					continue
				}
			}
			encoded, found, err := s.readIndexValueReader(reader, namespace, observationTimelinePageID(hourID, cursor.page), maxObservationTimelinePage)
			if err != nil {
				return err
			}
			if !found {
				return ErrCorrupt
			}
			entries, err = decodeObservationTimelinePage(encoded, hourID)
			clear(encoded)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if _, duplicate := cursor.hourSeen[entry.id]; duplicate {
					return ErrCorrupt
				}
				cursor.hourSeen[entry.id] = struct{}{}
			}
			cursor.rowsRead += len(entries)
			cursor.page++
			if cursor.page == cursor.pages {
				if cursor.rowsRead != cursor.expected {
					return ErrCorrupt
				}
				cursor.hour = cursor.hour.Add(-time.Hour)
				cursor.initialized = false
				cursor.hourSeen = nil
			}
			return nil
		}
		return nil
	})
	if err != nil {
		return nil, cursor, err
	}
	if cursor.hour.Before(cursor.lastHour) {
		cursor.done = true
	}
	return entries, cursor, nil
}

// ListObservationWindow reads primary evidence independently of activity and
// summarisation. It scans authenticated directory pages in bounded read
// quanta, carrying deduplication state until global ordering is known.
func (s *Store) ListObservationWindow(ctx context.Context, request storage.ObservationWindowRequest) (storage.ObservationWindow, error) {
	if s == nil || s.state == nil {
		return storage.ObservationWindow{}, &storage.StoreError{Code: storage.Closed}
	}
	s.state.publicationMu.RLock()
	defer s.state.publicationMu.RUnlock()
	return s.listObservationWindow(ctx, request)
}

// listObservationWindow requires publicationMu.RLock for the full multi-
// transaction scan so no writer can change editions between scan quanta.
func (s *Store) listObservationWindow(ctx context.Context, request storage.ObservationWindowRequest) (storage.ObservationWindow, error) {
	if ctx == nil || !storage.ValidObservationWindow(request) {
		return storage.ObservationWindow{}, &storage.StoreError{Code: storage.Invalid}
	}
	page := storage.ObservationWindow{Observations: make([]capture.SanitizedObservation, 0, request.Limit), CapturedAt: make([]time.Time, 0, request.Limit)}
	appDigest := s.searchLookupDigest(searchIndexApp, request.AppID)
	hostDigest := s.searchLookupDigest(searchIndexDomain, request.Domain)
	excluded := make(map[[32]byte]struct{}, len(request.ExcludeAppIDs))
	for _, app := range request.ExcludeAppIDs {
		excluded[s.searchLookupDigest(searchIndexApp, app)] = struct{}{}
	}
	start, end := request.Start.UTC(), request.End.UTC()
	cursor := newObservationTimelineScanCursor(start, end)
	candidates := make([]captureWindowCandidate, 0, request.Limit+1)
	keep := request.Limit + 1
	seen := make(map[string]struct{})
	for !cursor.done {
		if err := ctx.Err(); err != nil {
			return storage.ObservationWindow{}, err
		}
		entries, next, err := s.observationTimelineQuantum(ctx, observationTimelineNS, cursor)
		if err != nil {
			return storage.ObservationWindow{}, err
		}
		cursor = next
		if len(entries) == 0 {
			continue
		}
		err = s.viewWithReadLimit(ctx, bolt.MaxReadTransactionOps, func(reader bolt.Reader) error {
			for _, entry := range entries {
				if _, duplicate := seen[entry.id]; duplicate {
					continue
				}
				if !entry.at.Before(end) || !s.IsObservationTimeKnown(entry.at) {
					at, found, err := s.sightingAtBeforeReader(reader, entry.id, entry.at, end)
					if err != nil {
						return err
					}
					if !found {
						continue
					}
					entry.at = at
				}
				if entry.at.Before(start) || !entry.at.Before(end) || request.AppID != "" && entry.appDigest != appDigest || request.Domain != "" && entry.hostDigest != hostDigest {
					continue
				}
				if _, skip := excluded[entry.appDigest]; skip {
					continue
				}
				if entry.appDigest == s.searchLookupDigest(searchIndexApp, "sh.blur.notes") {
					current, err := s.isCurrentNoteSourceReader(reader, entry.id)
					if err != nil {
						return err
					}
					if !current {
						continue
					}
				}
				seen[entry.id] = struct{}{}
				if !request.BeforeCapturedAt.IsZero() && (!entry.at.Before(request.BeforeCapturedAt) && (!entry.at.Equal(request.BeforeCapturedAt) || entry.id >= request.BeforeSourceID)) {
					continue
				}
				candidates = append(candidates, entry)
				if len(candidates) > keep {
					sort.Slice(candidates, func(i, j int) bool {
						if !candidates[i].at.Equal(candidates[j].at) {
							return candidates[i].at.After(candidates[j].at)
						}
						return candidates[i].id > candidates[j].id
					})
					candidates = candidates[:keep]
				}
			}
			return nil
		})
		if err != nil {
			return storage.ObservationWindow{}, err
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].at.Equal(candidates[j].at) {
			return candidates[i].at.After(candidates[j].at)
		}
		return candidates[i].id > candidates[j].id
	})
	err := s.viewWithReadLimit(ctx, bolt.MaxReadTransactionOps, func(reader bolt.Reader) error {
		for _, candidate := range candidates {
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(page.Observations) >= request.Limit {
				page.HasMore = true
				break
			}
			observation, found, err := s.getObservationReader(reader, candidate.id)
			if err != nil {
				return err
			}
			if !found || request.AppID != "" && observation.AppID() != request.AppID || request.Domain != "" && observation.Domain() != request.Domain {
				return ErrCorrupt
			}
			at, err := s.observationCapturedAtReader(reader, candidate.id)
			if err != nil {
				return err
			}
			if err := s.verifySightingReader(reader, candidate.id, candidate.at, at); err != nil {
				return err
			}
			bytes := observationSearchBytes(observation)
			if bytes > request.MaxBytes-page.Bytes {
				page.HasMore = true
				break
			}
			page.Observations = append(page.Observations, observation)
			page.CapturedAt = append(page.CapturedAt, candidate.at)
			page.Bytes += bytes
			page.NextCapturedAt, page.NextSourceID = candidate.at, candidate.id
		}
		return nil
	})
	if err != nil {
		return storage.ObservationWindow{}, err
	}
	return page, nil
}

func (tx *Tx) indexObservationTimelineLocked(observation capture.SanitizedObservation, at time.Time) error {
	sourceID := observation.ContentID()
	if !validHash(sourceID) || !validCaptureTime(at) {
		return &storage.StoreError{Code: storage.Invalid}
	}
	ref, found, err := tx.readIndexValueLocked(observationTimelineSourceNS, sourceID, 64)
	if err != nil {
		return err
	}
	if found {
		hourID, _, err := parseObservationTimelinePageID(string(ref))
		clear(ref)
		if err != nil || hourID != observationTimelineHourID(at) {
			return ErrCorrupt
		}
		return nil // resumable backfill may revisit an already committed source
	}
	pageID, err := tx.appendObservationTimelineLocked(observation, at)
	if err != nil {
		return err
	}
	return tx.putIndexValueLocked(observationTimelineSourceNS, sourceID, []byte(pageID))
}

func (tx *Tx) appendObservationTimelineLocked(observation capture.SanitizedObservation, at time.Time) (string, error) {
	sourceID := observation.ContentID()
	hourID := observationTimelineHourID(at)
	encoded, found, err := tx.readIndexValueLocked(observationTimelineNS, hourID, 9)
	if err != nil {
		return "", err
	}
	meta := observationTimelineMeta{pages: 1}
	var entries []captureWindowCandidate
	if found {
		meta, err = decodeObservationTimelineMeta(encoded)
		clear(encoded)
		if err != nil {
			return "", err
		}
		encoded, found, err = tx.readIndexValueLocked(observationTimelineNS, observationTimelinePageID(hourID, meta.pages-1), maxObservationTimelinePage)
		if err != nil {
			return "", err
		}
		if !found {
			return "", ErrCorrupt
		}
		entries, err = decodeObservationTimelinePage(encoded, hourID)
		clear(encoded)
		if err != nil {
			return "", err
		}
	}
	if int64(meta.count) == maxTimelineCounter {
		return "", &storage.StoreError{Code: storage.Incomplete}
	}
	if len(entries) == observationTimelinePageSize {
		if int64(meta.pages) == maxTimelineCounter {
			return "", &storage.StoreError{Code: storage.Incomplete}
		}
		meta.pages++
		entries = nil
	}
	entries = append(entries, captureWindowCandidate{id: sourceID, at: at.UTC(), appDigest: tx.store.searchLookupDigest(searchIndexApp, observation.AppID()), hostDigest: tx.store.searchLookupDigest(searchIndexDomain, observation.Domain())})
	meta.count++
	pageID := observationTimelinePageID(hourID, meta.pages-1)
	encoded = encodeObservationTimelinePage(entries)
	defer clear(encoded)
	if err := tx.putIndexValueLocked(observationTimelineNS, pageID, encoded); err != nil {
		return "", err
	}
	if err := tx.putIndexValueLocked(observationTimelineNS, hourID, encodeObservationTimelineMeta(meta)); err != nil {
		return "", err
	}
	return pageID, nil
}

func (tx *Tx) removeObservationTimelineSource(sourceID string) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if err := tx.checkLocked(); err != nil {
		return tx.failLocked(err)
	}
	ref, found, err := tx.readIndexValueLocked(observationTimelineSourceNS, sourceID, 64)
	if err != nil {
		return err
	}
	if !found {
		return ErrCorrupt
	}
	pageID := string(ref)
	clear(ref)
	hourID, _, err := parseObservationTimelinePageID(pageID)
	if err != nil {
		return err
	}
	encoded, found, err := tx.readIndexValueLocked(observationTimelineNS, pageID, maxObservationTimelinePage)
	if err != nil || !found {
		return ErrCorrupt
	}
	entries, err := decodeObservationTimelinePage(encoded, hourID)
	clear(encoded)
	if err != nil {
		return err
	}
	index := -1
	for i, entry := range entries {
		if entry.id == sourceID {
			index = i
			break
		}
	}
	if index < 0 {
		return ErrCorrupt
	}
	entries = append(entries[:index], entries[index+1:]...)
	encoded, found, err = tx.readIndexValueLocked(observationTimelineNS, hourID, 9)
	if err != nil || !found {
		return ErrCorrupt
	}
	meta, err := decodeObservationTimelineMeta(encoded)
	clear(encoded)
	if err != nil || meta.count < 1 {
		return ErrCorrupt
	}
	meta.count--
	encoded = encodeObservationTimelinePage(entries)
	defer clear(encoded)
	if err := tx.putIndexValueLocked(observationTimelineNS, pageID, encoded); err != nil {
		return err
	}
	if err := tx.putIndexValueLocked(observationTimelineNS, hourID, encodeObservationTimelineMeta(meta)); err != nil {
		return err
	}
	key := tx.store.logicalKey(groupRecord, kindIndex, "index:"+observationTimelineSourceNS, sourceID)
	defer clear(key)
	return tx.deleteEntryLocked(key)
}

func observationTimelineHourID(at time.Time) string { return "hour:" + at.UTC().Format("20060102T15Z") }

func observationTimelinePageID(hourID string, page int) string {
	return fmt.Sprintf("%s:%04d", hourID, page)
}

func parseObservationTimelinePageID(id string) (string, int, error) {
	separator := strings.LastIndexByte(id, ':')
	if separator < 0 || !validChronicleHourID(id[:separator]) {
		return "", 0, ErrCorrupt
	}
	page, err := strconv.Atoi(id[separator+1:])
	if err != nil || page < 0 || int64(page) >= maxTimelineCounter || observationTimelinePageID(id[:separator], page) != id {
		return "", 0, ErrCorrupt
	}
	return id[:separator], page, nil
}

func encodeObservationTimelineMeta(meta observationTimelineMeta) []byte {
	out := make([]byte, 9)
	out[0] = 1
	binary.BigEndian.PutUint32(out[1:5], uint32(meta.pages))
	binary.BigEndian.PutUint32(out[5:9], uint32(meta.count))
	return out
}

func decodeObservationTimelineMeta(encoded []byte) (observationTimelineMeta, error) {
	if len(encoded) != 9 || encoded[0] != 1 {
		return observationTimelineMeta{}, ErrCorrupt
	}
	meta := observationTimelineMeta{pages: int(binary.BigEndian.Uint32(encoded[1:5])), count: int(binary.BigEndian.Uint32(encoded[5:9]))}
	if meta.pages < 1 || int64(meta.pages) > maxTimelineCounter || meta.count < 0 || int64(meta.count) > int64(meta.pages)*observationTimelinePageSize {
		return observationTimelineMeta{}, ErrCorrupt
	}
	return meta, nil
}

func encodeObservationTimelinePage(entries []captureWindowCandidate) []byte {
	out := make([]byte, 5+len(entries)*observationTimelineEntryBytes)
	out[0] = 1
	binary.BigEndian.PutUint32(out[1:5], uint32(len(entries)))
	for index, entry := range entries {
		offset := 5 + index*observationTimelineEntryBytes
		_, _ = hex.Decode(out[offset:offset+32], []byte(entry.id))
		binary.BigEndian.PutUint64(out[offset+32:offset+40], uint64(entry.at.UnixNano()))
		copy(out[offset+40:offset+72], entry.appDigest[:])
		copy(out[offset+72:offset+104], entry.hostDigest[:])
	}
	return out
}

func decodeObservationTimelinePage(encoded []byte, hourID string) ([]captureWindowCandidate, error) {
	if len(encoded) < 5 || encoded[0] != 1 {
		return nil, ErrCorrupt
	}
	count := int(binary.BigEndian.Uint32(encoded[1:5]))
	if count > observationTimelinePageSize || len(encoded) != 5+count*observationTimelineEntryBytes {
		return nil, ErrCorrupt
	}
	entries := make([]captureWindowCandidate, 0, count)
	seen := make(map[string]struct{}, count)
	for index := 0; index < count; index++ {
		offset := 5 + index*observationTimelineEntryBytes
		entry := captureWindowCandidate{id: hex.EncodeToString(encoded[offset : offset+32]), at: time.Unix(0, int64(binary.BigEndian.Uint64(encoded[offset+32:offset+40]))).UTC()}
		copy(entry.appDigest[:], encoded[offset+40:offset+72])
		copy(entry.hostDigest[:], encoded[offset+72:offset+104])
		if _, duplicate := seen[entry.id]; duplicate || !validCaptureTime(entry.at) || observationTimelineHourID(entry.at) != hourID {
			return nil, ErrCorrupt
		}
		seen[entry.id] = struct{}{}
		entries = append(entries, entry)
	}
	return entries, nil
}

// ensureObservationTimeline resumes an authenticated source-ID cursor. It
// indexes existing source timestamps in bounded transactions before capture or
// readers can use the opened store; text is never sent to an external service.
func (s *Store) ensureObservationTimeline(ctx context.Context) error {
	state, found, err := s.readIndexValue(ctx, captureTimeMigrationNamespace, observationTimelineMarkerID, 66)
	if err != nil {
		return err
	}
	after := ""
	if found {
		if (len(state) != 2 && len(state) != 66) || state[0] != 1 || state[1] > 1 {
			clear(state)
			return ErrCorrupt
		}
		complete := state[1] == 1
		if len(state) == 66 {
			after = string(state[2:])
			if !validHash(after) || complete {
				clear(state)
				return ErrCorrupt
			}
		}
		clear(state)
		if complete {
			return nil
		}
	}
	for {
		page, err := s.ListObservationPage(ctx, storage.ObservationPageRequest{After: after, Limit: storage.MaxObservationPageRows, MaxBytes: storage.MaxObservationPageBytes})
		if err != nil {
			return err
		}
		if len(page.Observations) != len(page.CapturedAt) || len(page.Observations) == 0 && page.HasMore {
			return ErrCorrupt
		}
		if len(page.Observations) > 0 {
			after = page.Observations[len(page.Observations)-1].ContentID()
		}
		err = s.Update(ctx, func(tx *Tx) error {
			tx.mu.Lock()
			defer tx.mu.Unlock()
			for index, observation := range page.Observations {
				if err := tx.indexObservationTimelineLocked(observation, page.CapturedAt[index]); err != nil {
					return tx.failLocked(err)
				}
			}
			marker := []byte{1, 1}
			if page.HasMore {
				marker = append([]byte{1, 0}, []byte(after)...)
			}
			defer clear(marker)
			return tx.putIndexValueLocked(captureTimeMigrationNamespace, observationTimelineMarkerID, marker)
		})
		if err != nil {
			return err
		}
		if !page.HasMore {
			return nil
		}
	}
}
