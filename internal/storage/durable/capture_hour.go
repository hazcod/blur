package durable

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"sort"
	"time"

	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"github.com/hazcod/blur/internal/storage/recordcodec"
)

const (
	captureHourNS            = "capture-hour"
	captureHourSourceNS      = "capture-hour-src"
	maxSourcesPerCaptureHour = 64
)

// CaptureHourEvidence reads only the encrypted source IDs indexed for one UTC
// hour. It never enumerates apps, files, browser history, or neighboring hours.
func (s *Store) CaptureHourEvidence(ctx context.Context, start time.Time) (chronicle.Hour, error) {
	var empty chronicle.Hour
	if ctx == nil || start.IsZero() || start.Location() != time.UTC || !start.Equal(start.Truncate(time.Hour)) {
		return empty, &storage.StoreError{Code: storage.Invalid}
	}
	end := start.Add(time.Hour)
	id := "hour:" + start.Format("20060102T15Z")
	hour := chronicle.Hour{Start: start, End: end}
	err := s.view(ctx, func(reader bolt.Reader) error {
		encoded, found, err := s.readIndexValueReader(reader, captureHourNS, id, 8192)
		if err != nil || !found {
			return err
		}
		defer clear(encoded)
		var ids []string
		if json.Unmarshal(encoded, &ids) != nil || len(ids) > maxSourcesPerCaptureHour || !validSourceHashIDs(ids) {
			return ErrCorrupt
		}
		for _, sourceID := range ids {
			observation, found, err := s.getObservationReader(reader, sourceID)
			if err != nil {
				return err
			}
			if !found {
				return ErrCorrupt
			}
			at, err := s.observationCapturedAtReader(reader, sourceID)
			if err != nil {
				return err
			}
			if at.Before(start) || !at.Before(end) {
				return ErrCorrupt
			}
			if len(observation.Text()) > chronicle.MaxEvidenceItemBytes {
				continue
			}
			score := min(9, 1+len(observation.Text())/128)
			hour.Evidence = append(hour.Evidence, chronicle.Evidence{Observation: observation, At: at, Salience: uint8(score)})
		}
		return nil
	})
	if err != nil {
		return empty, err
	}
	sort.Slice(hour.Evidence, func(i, j int) bool {
		if !hour.Evidence[i].At.Equal(hour.Evidence[j].At) {
			return hour.Evidence[i].At.Before(hour.Evidence[j].At)
		}
		return hour.Evidence[i].Observation.ContentID() < hour.Evidence[j].Observation.ContentID()
	})
	return hour, nil
}

func (tx *Tx) indexCaptureHourLocked(sourceID string, capturedAt time.Time) error {
	if tx == nil || !validHash(sourceID) || capturedAt.IsZero() {
		return &storage.StoreError{Code: storage.Invalid}
	}
	hourID := "hour:" + capturedAt.UTC().Format("20060102T15Z")
	encoded, found, err := tx.readIndexValueLocked(captureHourNS, hourID, 8192)
	if err != nil {
		return err
	}
	var ids []string
	if found {
		if json.Unmarshal(encoded, &ids) != nil || len(ids) > maxSourcesPerCaptureHour || !validSourceHashIDs(ids) {
			clear(encoded)
			return ErrCorrupt
		}
		clear(encoded)
	}
	index := sort.SearchStrings(ids, sourceID)
	if index < len(ids) && ids[index] == sourceID {
		return ErrCorrupt
	}
	if len(ids) >= maxSourcesPerCaptureHour {
		return nil
	}
	ids = append(ids, "")
	copy(ids[index+1:], ids[index:])
	ids[index] = sourceID
	if err := tx.putIndexValueLocked(captureHourNS, hourID, mustJSON(ids)); err != nil {
		return err
	}
	return tx.putIndexValueLocked(captureHourSourceNS, sourceID, []byte(hourID))
}

func (tx *Tx) putIndexValueLocked(namespace, id string, value []byte) error {
	if len(value) > recordcodec.MaxPlaintextBytes-5 {
		return &storage.StoreError{Code: storage.Invalid}
	}
	plain := make([]byte, 5+len(value))
	plain[0] = valueVersion
	binary.BigEndian.PutUint32(plain[1:5], uint32(len(value)))
	copy(plain[5:], value)
	defer clear(plain)
	return tx.putRecordLocked(kindIndex, "index:"+namespace, id, plain, true)
}

func (tx *Tx) removeCaptureHourSource(sourceID string) error {
	encoded, found, err := tx.readIndexValue(captureHourSourceNS, sourceID, 64)
	if err != nil || !found {
		return err
	}
	hourID := string(encoded)
	clear(encoded)
	if !validChronicleHourID(hourID) {
		return ErrCorrupt
	}
	ids, found, err := tx.readIndexValue(captureHourNS, hourID, 8192)
	if err != nil || !found {
		return ErrCorrupt
	}
	defer clear(ids)
	var sources []string
	if json.Unmarshal(ids, &sources) != nil || !validSourceHashIDs(sources) {
		return ErrCorrupt
	}
	index := sort.SearchStrings(sources, sourceID)
	if index >= len(sources) || sources[index] != sourceID {
		return ErrCorrupt
	}
	sources = append(sources[:index], sources[index+1:]...)
	if len(sources) == 0 {
		if err := tx.deleteKnowledgeRecord(captureHourNS, hourID); err != nil {
			return err
		}
	} else if err := tx.PutIndexRecord(captureHourNS, hourID, mustJSON(sources)); err != nil {
		return err
	}
	return tx.deleteKnowledgeRecord(captureHourSourceNS, sourceID)
}

func (tx *Tx) readIndexValueLocked(namespace, id string, maxBytes int) ([]byte, bool, error) {
	if tx == nil || !validNamespace(namespace) || !validOpaqueID(id) || maxBytes < 1 {
		return nil, false, &storage.StoreError{Code: storage.Invalid}
	}
	key := tx.store.logicalKey(groupRecord, kindIndex, "index:"+namespace, id)
	defer clear(key)
	stored, found, err := tx.writer.Get(key)
	if err != nil {
		return nil, false, mapStorePhysicalError(err)
	}
	if !found {
		return nil, false, nil
	}
	if len(stored) > maxBytes+1024 {
		clear(stored)
		return nil, false, ErrCorrupt
	}
	plain, err := tx.store.open(key, "index:"+namespace, stored)
	clear(stored)
	if err != nil {
		return nil, false, ErrCorrupt
	}
	defer clear(plain)
	if len(plain) < 5 || plain[0] != valueVersion || int(binary.BigEndian.Uint32(plain[1:5])) != len(plain)-5 || len(plain)-5 > maxBytes {
		return nil, false, ErrCorrupt
	}
	return append([]byte(nil), plain[5:]...), true, nil
}

func validSourceHashIDs(ids []string) bool {
	for i, id := range ids {
		if !validHash(id) || i > 0 && ids[i-1] >= id {
			return false
		}
	}
	return true
}
