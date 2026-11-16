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
	observationTimelineNS         = "observation-time"
	observationTimelineSourceNS   = "observation-time-src"
	observationTimelineMarkerID   = "observation-timeline-v1"
	observationTimelinePageSize   = 64
	maxObservationTimelinePages   = 1024
	maxObservationTimelineRows    = observationTimelinePageSize * maxObservationTimelinePages
	observationTimelineEntryBytes = 104 // content hash, nanoseconds, keyed app/domain digests
	maxObservationTimelinePage    = 5 + observationTimelinePageSize*observationTimelineEntryBytes
	maxObservationTimelineReads   = bolt.MaxReadTransactionOps - storage.MaxObservationPageRows*4 - 8
)

type captureWindowCandidate struct {
	id                    string
	at                    time.Time
	appDigest, hostDigest [32]byte
}

type observationTimelineMeta struct{ pages, count int }

// ListObservationWindow reads primary evidence independently of activity and
// summarisation. Hour directories and pages are authenticated and encrypted;
// physical keys reveal neither timestamps nor source identities. Dense ranges
// fail explicitly at the metadata budget instead of returning a partial rank.
func (s *Store) ListObservationWindow(ctx context.Context, request storage.ObservationWindowRequest) (storage.ObservationWindow, error) {
	if ctx == nil || !storage.ValidObservationWindow(request) {
		return storage.ObservationWindow{}, &storage.StoreError{Code: storage.Invalid}
	}
	page := storage.ObservationWindow{Observations: make([]capture.SanitizedObservation, 0, request.Limit), CapturedAt: make([]time.Time, 0, request.Limit)}
	err := s.viewWithReadLimit(ctx, bolt.MaxReadTransactionOps, func(reader bolt.Reader) error {
		appDigest := s.searchLookupDigest(searchIndexApp, request.AppID)
		hostDigest := s.searchLookupDigest(searchIndexDomain, request.Domain)
		excluded := make(map[[32]byte]struct{}, len(request.ExcludeAppIDs))
		for _, app := range request.ExcludeAppIDs {
			excluded[s.searchLookupDigest(searchIndexApp, app)] = struct{}{}
		}
		start, end := request.Start.UTC(), request.End.UTC()
		firstHour, lastHour := end.Add(-time.Nanosecond).Truncate(time.Hour), start.Truncate(time.Hour)
		// Re-scan newer partitions before applying the cursor: a source may
		// have an older visit that must not reappear on a later page.
		candidates := make([]captureWindowCandidate, 0, request.Limit+1)
		seen := make(map[string]struct{})
		inspected := 0
		reads := 0
		for hourStart := firstHour; !hourStart.Before(lastHour); hourStart = hourStart.Add(-time.Hour) {
			if err := ctx.Err(); err != nil {
				return err
			}
			hourID := observationTimelineHourID(hourStart)
			if reads >= maxObservationTimelineReads {
				return &storage.StoreError{Code: storage.Incomplete}
			}
			reads++
			encoded, found, err := s.readIndexValueReader(reader, observationTimelineNS, hourID, 9)
			if err != nil {
				return err
			}
			if !found {
				continue
			}
			meta, err := decodeObservationTimelineMeta(encoded)
			clear(encoded)
			if err != nil {
				return err
			}
			if meta.count > maxObservationTimelineRows-inspected {
				return &storage.StoreError{Code: storage.Incomplete}
			}
			if meta.count == 0 {
				continue
			}
			hourRows := 0
			hourSeen := make(map[string]struct{})
			for index := 0; index < meta.pages; index++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				if reads >= maxObservationTimelineReads {
					return &storage.StoreError{Code: storage.Incomplete}
				}
				reads++
				encoded, found, err := s.readIndexValueReader(reader, observationTimelineNS, observationTimelinePageID(hourID, index), maxObservationTimelinePage)
				if err != nil {
					return err
				}
				if !found {
					return ErrCorrupt
				}
				entries, err := decodeObservationTimelinePage(encoded, hourID)
				clear(encoded)
				if err != nil {
					return err
				}
				hourRows += len(entries)
				if hourRows > meta.count {
					return ErrCorrupt
				}
				for _, entry := range entries {
					if _, duplicate := hourSeen[entry.id]; duplicate {
						return ErrCorrupt
					}
					hourSeen[entry.id] = struct{}{}
					if _, duplicate := seen[entry.id]; duplicate {
						continue
					}
					if !entry.at.Before(end) || !s.IsObservationTimeKnown(entry.at) {
						if reads >= maxObservationTimelineReads {
							return &storage.StoreError{Code: storage.Incomplete}
						}
						reads++
						at, found, err := s.sightingAtBeforeReader(reader, entry.id, entry.at, end)
						if err != nil {
							return err
						}
						if !found {
							continue
						}
						entry.at = at
					}
					if entry.at.Before(start) || !entry.at.Before(end) {
						continue
					}
					if request.AppID != "" && entry.appDigest != appDigest || request.Domain != "" && entry.hostDigest != hostDigest {
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
				}
			}
			if hourRows != meta.count {
				return ErrCorrupt
			}
			inspected += hourRows
			// All remaining partitions are older. One extra candidate proves
			// continuation without scanning irrelevant historical hours.
			if len(candidates) > request.Limit {
				break
			}
		}
		sort.Slice(candidates, func(i, j int) bool {
			if !candidates[i].at.Equal(candidates[j].at) {
				return candidates[i].at.After(candidates[j].at)
			}
			return candidates[i].id > candidates[j].id
		})
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
	if len(entries) == observationTimelinePageSize {
		if meta.pages == maxObservationTimelinePages {
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
	if err != nil || page < 0 || page >= maxObservationTimelinePages || observationTimelinePageID(id[:separator], page) != id {
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
	if meta.pages < 1 || meta.pages > maxObservationTimelinePages || meta.count > meta.pages*observationTimelinePageSize {
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
