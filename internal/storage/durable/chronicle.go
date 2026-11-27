package durable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
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
	maxChronicleSources = 1 // legacy singleton reverse receipts; current links are paged
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
	dependencies := summary.DependencyIDs()
	return s.Update(ctx, func(tx *Tx) error {
		if j, pending, err := s.chronicleErasureReader(tx.writer); err != nil {
			return err
		} else if pending && j.Hours {
			return errChronicleErasurePending
		}
		var followUpAt time.Time
		var priorSources []string
		admission := &chronicleAdmissionReader{Reader: tx.writer}
		defer admission.close()
		for _, sourceID := range dependencies {
			capturedAt, found, err := s.sourceAdmissionReader(admission, sourceID)
			if err != nil {
				return err
			} else if !found {
				return &storage.StoreError{Code: storage.Invalid}
			}
			visited, observed, err := s.chronicleSourceVisitReader(ctx, admission, sourceID, capturedAt, summary.Start, summary.End)
			if err != nil {
				return err
			}
			if !observed {
				return &storage.StoreError{Code: storage.Invalid}
			}
			if summary.FollowUp != nil && sourceID == summary.FollowUp.SourceID {
				observation, _, found, err := s.sourceEvidenceReader(admission, sourceID)
				if err != nil {
					return err
				}
				if !found {
					return &storage.StoreError{Code: storage.Invalid}
				}
				if !chronicle.SupportsFollowUpSource(observation) {
					return &storage.StoreError{Code: storage.Invalid}
				}
				followUpAt = visited
			}
		}
		admission.close() // release the read-only cache before any projection write
		projection := newChronicleProjectionWriter(tx.writer)
		tx.writer = projection
		defer func() {
			tx.writer = projection.Writer
			projection.cache.close()
		}()
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
			priorSources = old.DependencyIDs()
			if old.FollowUp != nil && (summary.FollowUp == nil || chronicleFollowUpID(old.HourID, old.FollowUp.SourceID, old.FollowUp.Text) != chronicleFollowUpID(summary.HourID, summary.FollowUp.SourceID, summary.FollowUp.Text)) {
				if err := tx.removeChronicleFollowUp(old); err != nil {
					return err
				}
			}
			for _, sourceID := range priorSources {
				if slices.Contains(dependencies, sourceID) {
					continue
				}
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
		for _, sourceID := range dependencies {
			if slices.Contains(priorSources, sourceID) {
				continue
			}
			if err := tx.addChronicleSource(sourceID, summary.HourID); err != nil {
				return err
			}
		}
		if err := tx.PutIndexRecord(chronicleNS, summary.HourID, encoded); err != nil {
			return err
		}
		if err := tx.markChronicleDayDirty(summary.Start); err != nil {
			return err
		}
		if summary.FollowUp != nil {
			return tx.putChronicleFollowUp(summary, followUpAt)
		}
		return nil
	})
}

func (tx *Tx) putChronicleFollowUp(summary chronicle.Summary, at time.Time) error {
	if tx == nil || at.IsZero() || summary.FollowUp == nil {
		return &storage.StoreError{Code: storage.Invalid}
	}
	followUp := *summary.FollowUp
	dependencies := summary.DependencyIDs()
	questionID := chronicleFollowUpID(summary.HourID, followUp.SourceID, followUp.Text)
	// The same deterministic question can have been saved under a different
	// local zone. Refresh its actual model dependencies wherever it exists,
	// retaining the user's resolution and separately authored answer.
	dates, err := tx.chronicleFollowUpDates(summary)
	if err != nil {
		return err
	}
	updated := false
	for _, date := range dates {
		snapshot, found, err := tx.readChronicleFollowUpSnapshot(date)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		for i := range snapshot.Questions {
			if snapshot.Questions[i].ID != questionID {
				continue
			}
			snapshot.Questions[i].SourceIDs = append([]string(nil), dependencies...)
			if err := addChronicleFollowUpSources(&snapshot, dependencies); err != nil {
				return err
			}
			if err := tx.putTodaySnapshot(snapshot); err != nil {
				return err
			}
			updated = true
			break
		}
	}
	if updated {
		return nil
	}
	date, zone := localDate(at)
	snapshot, found, err := tx.readChronicleFollowUpSnapshot(date)
	if err != nil {
		return err
	}
	if !found {
		snapshot = today.Snapshot{Date: date, Zone: zone, GeneratedAt: at, GenerationID: "chronicle-followup-" + questionID}
	}
	if len(snapshot.Questions) >= today.MaxQuestions || !chronicleFollowUpSourcesFit(snapshot, dependencies) {
		return nil
	}
	if err := addChronicleFollowUpSources(&snapshot, dependencies); err != nil {
		return err
	}
	snapshot.Questions = append(snapshot.Questions, today.Question{
		ID: questionID, Text: followUp.Text, Reason: "Possible missing follow-up after this meeting.",
		SourceIDs: dependencies, CreatedAt: at,
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
		if j, pending, err := s.chronicleErasureReader(reader); err != nil {
			return err
		} else if pending && j.Hours {
			return nil
		}
		b, found, err := s.readIndexValueReader(reader, chronicleNS, id, chronicle.MaxSummaryBytes+(16<<10))
		if err != nil || !found {
			return err
		}
		defer clear(b)
		if json.Unmarshal(b, &summary) != nil || !validChronicle(summary) || summary.HourID != id {
			return ErrCorrupt
		}
		known, err := s.chronicleTimeKnownReader(ctx, reader, summary)
		if err != nil {
			return err
		}
		if !known {
			summary = chronicle.Summary{}
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
		if j, pending, err := s.chronicleErasureReader(reader); err != nil {
			return err
		} else if pending && j.Hours {
			return nil
		}
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
				known, err := s.chronicleTimeKnownReader(ctx, reader, row)
				if err != nil {
					return err
				}
				if !known {
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

// Only the baseline's UTC hour can contain a summary dated by the old
// migration. Omit a mixed summary if any source has unknown original time;
// its text remains saved and the original sources stay available unscoped.
func (s *Store) chronicleTimeKnownReader(ctx context.Context, reader bolt.Reader, summary chronicle.Summary) (bool, error) {
	baseline := s.state.captureTimeBaseline
	if baseline.Before(summary.Start) || !baseline.Before(summary.End) {
		return true, nil
	}
	for _, id := range summary.DependencyIDs() {
		_, first, found, err := s.sourceEvidenceReader(reader, id)
		if err != nil {
			return false, err
		}
		if !found {
			return false, nil
		}
		_, observed, err := s.chronicleSourceVisitReader(ctx, reader, id, first, summary.Start, summary.End)
		if err != nil {
			return false, err
		}
		if !observed {
			return false, nil
		}
	}
	return true, nil
}

func (s *Store) DeleteAllChronicles(ctx context.Context) error {
	return s.eraseChronicles(ctx, true)
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
	if err := tx.removeChronicleFollowUp(summary); err != nil {
		return err
	}
	for _, sourceID := range summary.DependencyIDs() {
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
	if err := tx.deleteKnowledgeRecord(chronicleNS, id); err != nil {
		return err
	}
	if err := tx.invalidateRollupsCoveringHour(summary.Start); err != nil {
		return err
	}
	return tx.markChronicleDayDirty(summary.Start)
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
	root, _, err := tx.migrateChronicleLinks(sourceID)
	if err != nil {
		return err
	}
	return tx.appendChronicleLink(sourceID, id, &root)
}
func (tx *Tx) removeChronicleSource(sourceID, id string) error {
	root, found, err := tx.chronicleLinkRoot(sourceID)
	if err != nil {
		return err
	}
	if !found {
		// Removing a legacy singleton needs no linked-list migration. This also
		// keeps cleanup of a maximum-source legacy recap in one transaction.
		ids, present, err := tx.readChronicleSource(sourceID)
		if err != nil {
			return err
		}
		if !present || len(ids) != 1 || ids[0] != id {
			return ErrCorrupt
		}
		return tx.deleteKnowledgeRecord(chronicleSourceNS, sourceID)
	}
	return tx.unlinkChronicleLink(sourceID, id, root)
}
func (tx *Tx) removeChroniclesForSource(sourceID string) error {
	complete, err := tx.removeChroniclesForSourceBatch(sourceID)
	if err != nil {
		return err
	}
	if !complete {
		return ErrTransactionBudget
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
