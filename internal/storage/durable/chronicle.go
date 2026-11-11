package durable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"github.com/hazcod/blur/internal/today"
)

const (
	chronicleNS         = "chronicle"
	chronicleDayNS      = "chronicle-day"
	chronicleSourceNS   = "chronicle-src"
	chronicleDaysNS     = "chronicle-days"
	chronicleDaysID     = "all"
	maxChronicleSources = 1 // a source has one capture hour and one canonical summary
	maxChronicleDays    = 10000
	maxChronicleRange   = 32 * 24 * time.Hour
)

func (s *Store) PutChronicle(ctx context.Context, summary chronicle.Summary) error {
	if ctx == nil || !validChronicle(summary) {
		return &storage.StoreError{Code: storage.Invalid}
	}
	encoded, err := json.Marshal(summary)
	if err != nil || len(encoded) > chronicle.MaxSummaryBytes+(16<<10) {
		clear(encoded)
		return &storage.StoreError{Code: storage.Invalid}
	}
	defer clear(encoded)
	return s.Update(ctx, func(tx *Tx) error {
		var followUpAt time.Time
		for _, sourceID := range summary.SourceIDs {
			observation, found, err := s.getObservationReader(tx.writer, sourceID)
			if err != nil {
				return err
			} else if !found {
				return &storage.StoreError{Code: storage.Invalid}
			}
			capturedAt, err := s.observationCapturedAtReader(tx.writer, sourceID)
			if err != nil {
				return err
			}
			if capturedAt.Before(summary.Start) || !capturedAt.Before(summary.End) {
				return &storage.StoreError{Code: storage.Invalid}
			}
			if summary.FollowUp != nil && sourceID == summary.FollowUp.SourceID {
				if !chronicle.SupportsFollowUpEvidence(observation) {
					return &storage.StoreError{Code: storage.Invalid}
				}
				followUpAt = capturedAt
			}
		}
		oldBytes, found, err := tx.readIndexValue(chronicleNS, summary.HourID, chronicle.MaxSummaryBytes+(16<<10))
		if err != nil {
			return err
		}
		if found {
			var old chronicle.Summary
			if json.Unmarshal(oldBytes, &old) != nil || !validChronicle(old) {
				clear(oldBytes)
				return ErrCorrupt
			}
			clear(oldBytes)
			for _, sourceID := range old.SourceIDs {
				if err := tx.removeChronicleSource(sourceID, old.HourID); err != nil {
					return err
				}
			}
		} else {
			day := summary.Start.UTC().Format("2006-01-02")
			if err := tx.addChronicleDay(day); err != nil {
				return err
			}
			if err := tx.addChronicleDayHour(day, summary.HourID); err != nil {
				return err
			}
		}
		for _, sourceID := range summary.SourceIDs {
			if err := tx.addChronicleSource(sourceID, summary.HourID); err != nil {
				return err
			}
		}
		if err := tx.PutIndexRecord(chronicleNS, summary.HourID, encoded); err != nil {
			return err
		}
		if summary.FollowUp != nil {
			return tx.putChronicleFollowUp(summary.HourID, *summary.FollowUp, followUpAt)
		}
		return nil
	})
}

func (tx *Tx) putChronicleFollowUp(hourID string, followUp chronicle.FollowUp, at time.Time) error {
	if tx == nil || at.IsZero() || len(followUp.SourceID) != 64 || len(followUp.Text) == 0 {
		return &storage.StoreError{Code: storage.Invalid}
	}
	date, zone := localDate(at)
	questionID := chronicleFollowUpID(hourID, followUp.SourceID, followUp.Text)
	snapshot := today.Snapshot{}
	encoded, found, err := tx.readIndexValue(todayNamespace, date, today.MaxSnapshotBytes)
	if err != nil {
		return err
	}
	if found {
		if json.Unmarshal(encoded, &snapshot) != nil || snapshot.Date != date || snapshot.Validate() != nil {
			clear(encoded)
			return ErrCorrupt
		}
		clear(encoded)
	} else {
		snapshot = today.Snapshot{Date: date, Zone: zone, GeneratedAt: at, GenerationID: "chronicle-followup-" + questionID}
	}
	for _, existing := range snapshot.Questions {
		if existing.ID == questionID {
			return nil
		}
	}
	if len(snapshot.Questions) >= today.MaxQuestions {
		return nil
	}
	if !hasSource(snapshot.SourceIDs, followUp.SourceID) {
		if len(snapshot.SourceIDs) >= today.MaxSources {
			return nil
		}
		snapshot.SourceIDs = append(snapshot.SourceIDs, followUp.SourceID)
		sort.Strings(snapshot.SourceIDs)
	}
	snapshot.Questions = append(snapshot.Questions, today.Question{
		ID: questionID, Text: followUp.Text, Reason: "Possible missing follow-up after this meeting.",
		SourceIDs: []string{followUp.SourceID}, CreatedAt: at,
	})
	return tx.putTodaySnapshot(snapshot)
}

func chronicleFollowUpID(hourID, sourceID, text string) string {
	hash := sha256.Sum256([]byte("chronicle-follow-up-v1\x00" + hourID + "\x00" + sourceID + "\x00" + text))
	return "chronicle-question-" + hex.EncodeToString(hash[:])
}

func (s *Store) observationCapturedAtReader(reader bolt.Reader, sourceID string) (time.Time, error) {
	key := s.logicalKey(groupOrder, kindObservation, "", sourceID)
	defer clear(key)
	value, found, err := reader.Get(key)
	if err != nil {
		return time.Time{}, mapStorePhysicalError(err)
	}
	if !found {
		return time.Time{}, ErrCorrupt
	}
	_, capturedAt, recordKey, err := s.decodeSizeEntry(key, value)
	clear(value)
	clear(recordKey)
	if err != nil {
		return time.Time{}, ErrCorrupt
	}
	return capturedAt, nil
}

func (s *Store) GetChronicle(ctx context.Context, id string) (chronicle.Summary, bool, error) {
	if ctx == nil || !validChronicleHourID(id) {
		return chronicle.Summary{}, false, &storage.StoreError{Code: storage.Invalid}
	}
	var summary chronicle.Summary
	err := s.view(ctx, func(reader bolt.Reader) error {
		b, found, err := s.readIndexValueReader(reader, chronicleNS, id, chronicle.MaxSummaryBytes+(16<<10))
		if err != nil || !found {
			return err
		}
		defer clear(b)
		if json.Unmarshal(b, &summary) != nil || !validChronicle(summary) || summary.HourID != id {
			return ErrCorrupt
		}
		return nil
	})
	return summary, summary.HourID != "", err
}

// ListChronicles returns summaries in ascending UTC-hour order. The logical
// hour ID cursor remains stable across reopen and does not expose disk keys.
func (s *Store) ListChronicles(ctx context.Context, start, end time.Time, after string, limit int) ([]chronicle.Summary, string, error) {
	if ctx == nil || start.IsZero() || end.IsZero() || !start.Before(end) || end.Sub(start) > maxChronicleRange || limit < 1 || limit > 64 || after != "" && !validChronicleHourID(after) {
		return nil, "", &storage.StoreError{Code: storage.Invalid}
	}
	start, end = start.UTC(), end.UTC()
	first, last := start.Format("2006-01-02"), end.Add(-time.Nanosecond).Format("2006-01-02")
	var rows []chronicle.Summary
	var next string
	err := s.view(ctx, func(reader bolt.Reader) error {
		days, err := s.readChronicleDays(reader)
		if err != nil {
			return err
		}
		for _, day := range days {
			if day < first || day > last {
				continue
			}
			ids, err := s.readChronicleDay(reader, day)
			if err != nil {
				return err
			}
			for _, id := range ids {
				if after != "" && id <= after {
					continue
				}
				b, found, err := s.readIndexValueReader(reader, chronicleNS, id, chronicle.MaxSummaryBytes+(16<<10))
				if err != nil {
					return err
				}
				if !found {
					return ErrCorrupt
				}
				var row chronicle.Summary
				if json.Unmarshal(b, &row) != nil || !validChronicle(row) || row.HourID != id {
					clear(b)
					return ErrCorrupt
				}
				clear(b)
				if row.Start.Before(start) || !row.Start.Before(end) {
					continue
				}
				rows = append(rows, row)
				if len(rows) > limit {
					next = rows[limit-1].HourID
					rows = rows[:limit]
					return nil
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return rows, next, nil
}

func (s *Store) DeleteAllChronicles(ctx context.Context) error {
	if ctx == nil {
		return &storage.StoreError{Code: storage.Invalid}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		days, err := s.readChronicleDaysContext(ctx)
		if err != nil {
			return err
		}
		if len(days) == 0 {
			return nil
		}
		ids, err := s.readChronicleDayContext(ctx, days[0])
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return ErrCorrupt
		}
		for _, id := range ids {
			if err := s.DeleteChronicle(ctx, id); err != nil {
				return err
			}
		}
	}
}

func (s *Store) DeleteChronicle(ctx context.Context, id string) error {
	if ctx == nil || !validChronicleHourID(id) {
		return &storage.StoreError{Code: storage.Invalid}
	}
	return s.Update(ctx, func(tx *Tx) error { return tx.deleteChronicle(id) })
}

func (tx *Tx) deleteChronicle(id string) error {
	b, found, err := tx.readIndexValue(chronicleNS, id, chronicle.MaxSummaryBytes+(16<<10))
	if err != nil || !found {
		return err
	}
	var summary chronicle.Summary
	if json.Unmarshal(b, &summary) != nil || !validChronicle(summary) {
		clear(b)
		return ErrCorrupt
	}
	clear(b)
	for _, sourceID := range summary.SourceIDs {
		if err := tx.removeChronicleSource(sourceID, id); err != nil {
			return err
		}
	}
	day := summary.Start.UTC().Format("2006-01-02")
	ids, found, err := tx.readChronicleDay(day)
	if err != nil || !found {
		return ErrCorrupt
	}
	ids = removeString(ids, id)
	if len(ids) == 0 {
		if err := tx.deleteKnowledgeRecord(chronicleDayNS, day); err != nil {
			return err
		}
		days, found, err := tx.readChronicleDays()
		if err != nil || !found {
			return ErrCorrupt
		}
		days = removeString(days, day)
		if len(days) == 0 {
			if err := tx.deleteKnowledgeRecord(chronicleDaysNS, chronicleDaysID); err != nil {
				return err
			}
		} else if err := tx.PutIndexRecord(chronicleDaysNS, chronicleDaysID, mustJSON(days)); err != nil {
			return err
		}
	} else if err := tx.PutIndexRecord(chronicleDayNS, day, mustJSON(ids)); err != nil {
		return err
	}
	return tx.deleteKnowledgeRecord(chronicleNS, id)
}

func (tx *Tx) addChronicleDay(day string) error {
	days, _, err := tx.readChronicleDays()
	if err != nil {
		return err
	}
	i := sort.SearchStrings(days, day)
	if i < len(days) && days[i] == day {
		return nil
	}
	if len(days) >= maxChronicleDays {
		return &storage.StoreError{Code: storage.Invalid}
	}
	days = append(days, "")
	copy(days[i+1:], days[i:])
	days[i] = day
	if err := tx.PutIndexRecord(chronicleDaysNS, chronicleDaysID, mustJSON(days)); err != nil {
		return err
	}
	return tx.PutIndexRecord(chronicleDayNS, day, []byte("[]"))
}

func (tx *Tx) addChronicleDayHour(day, id string) error {
	ids, found, err := tx.readChronicleDay(day)
	if err != nil {
		return err
	}
	if !found {
		return ErrCorrupt
	}
	i := sort.SearchStrings(ids, id)
	if i < len(ids) && ids[i] == id {
		return ErrCorrupt
	}
	if len(ids) >= 24 {
		return &storage.StoreError{Code: storage.Invalid}
	}
	ids = append(ids, "")
	copy(ids[i+1:], ids[i:])
	ids[i] = id
	return tx.PutIndexRecord(chronicleDayNS, day, mustJSON(ids))
}

func (tx *Tx) addChronicleSource(sourceID, id string) error {
	ids, found, err := tx.readChronicleSource(sourceID)
	if err != nil {
		return err
	}
	i := sort.SearchStrings(ids, id)
	if i < len(ids) && ids[i] == id {
		return ErrCorrupt
	}
	if len(ids) >= maxChronicleSources {
		return &storage.StoreError{Code: storage.Invalid}
	}
	ids = append(ids, "")
	copy(ids[i+1:], ids[i:])
	ids[i] = id
	_ = found
	return tx.PutIndexRecord(chronicleSourceNS, sourceID, mustJSON(ids))
}
func (tx *Tx) removeChronicleSource(sourceID, id string) error {
	ids, found, err := tx.readChronicleSource(sourceID)
	if err != nil {
		return err
	}
	if !found {
		return ErrCorrupt
	}
	ids = removeString(ids, id)
	if len(ids) == 0 {
		return tx.deleteKnowledgeRecord(chronicleSourceNS, sourceID)
	}
	return tx.PutIndexRecord(chronicleSourceNS, sourceID, mustJSON(ids))
}

func (tx *Tx) removeChroniclesForSource(sourceID string) error {
	ids, found, err := tx.readChronicleSource(sourceID)
	if err != nil || !found {
		return err
	}
	for _, id := range ids {
		if err := tx.deleteChronicle(id); err != nil {
			return err
		}
	}
	return nil
}
func (tx *Tx) readChronicleSource(id string) ([]string, bool, error) {
	b, found, err := tx.readIndexValue(chronicleSourceNS, id, 8192)
	if err != nil || !found {
		return nil, found, err
	}
	defer clear(b)
	var ids []string
	if json.Unmarshal(b, &ids) != nil || len(ids) > maxChronicleSources || !validChronicleIDs(ids) {
		return nil, false, ErrCorrupt
	}
	return ids, true, nil
}
func (tx *Tx) readChronicleDay(day string) ([]string, bool, error) {
	b, found, err := tx.readIndexValue(chronicleDayNS, day, 4096)
	if err != nil || !found {
		return nil, found, err
	}
	defer clear(b)
	var ids []string
	if json.Unmarshal(b, &ids) != nil || len(ids) > 24 || !validChronicleIDs(ids) {
		return nil, false, ErrCorrupt
	}
	return ids, true, nil
}
func (tx *Tx) readChronicleDays() ([]string, bool, error) {
	b, found, err := tx.readIndexValue(chronicleDaysNS, chronicleDaysID, 200<<10)
	if err != nil || !found {
		return nil, found, err
	}
	defer clear(b)
	var days []string
	if json.Unmarshal(b, &days) != nil || len(days) > maxChronicleDays || !validChronicleDays(days) {
		return nil, false, ErrCorrupt
	}
	return days, true, nil
}
func (s *Store) readChronicleDaysContext(ctx context.Context) ([]string, error) {
	var days []string
	err := s.view(ctx, func(r bolt.Reader) error { var e error; days, e = s.readChronicleDays(r); return e })
	return days, err
}
func (s *Store) readChronicleDayContext(ctx context.Context, day string) ([]string, error) {
	var ids []string
	err := s.view(ctx, func(r bolt.Reader) error {
		var e error
		ids, e = s.readChronicleDay(r, day)
		return e
	})
	return ids, err
}
func (s *Store) readChronicleDays(r bolt.Reader) ([]string, error) {
	b, found, err := s.readIndexValueReader(r, chronicleDaysNS, chronicleDaysID, 200<<10)
	if err != nil || !found {
		return nil, err
	}
	defer clear(b)
	var days []string
	if json.Unmarshal(b, &days) != nil || len(days) > maxChronicleDays || !validChronicleDays(days) {
		return nil, ErrCorrupt
	}
	return days, nil
}
func (s *Store) readChronicleDay(r bolt.Reader, day string) ([]string, error) {
	b, found, err := s.readIndexValueReader(r, chronicleDayNS, day, 4096)
	if err != nil || !found {
		return nil, ErrCorrupt
	}
	defer clear(b)
	var ids []string
	if json.Unmarshal(b, &ids) != nil || len(ids) > 24 || !validChronicleIDs(ids) {
		return nil, ErrCorrupt
	}
	return ids, nil
}

func validChronicle(s chronicle.Summary) bool {
	return chronicle.ValidatePersistedSummary(s) == nil
}
func validChronicleHourID(id string) bool {
	t, err := time.Parse("hour:20060102T15Z", id)
	return err == nil && id == "hour:"+t.UTC().Format("20060102T15Z")
}
func validChronicleIDs(ids []string) bool {
	for i, id := range ids {
		if !validChronicleHourID(id) || i > 0 && ids[i-1] >= id {
			return false
		}
	}
	return true
}
func validChronicleDays(days []string) bool {
	for i, d := range days {
		if !validTodayDate(d) || i > 0 && days[i-1] >= d {
			return false
		}
	}
	return true
}
