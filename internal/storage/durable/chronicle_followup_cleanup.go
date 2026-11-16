package durable

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"github.com/hazcod/blur/internal/storage/recordcodec"
	"github.com/hazcod/blur/internal/today"
)

// Every generated question includes its cited source, including legacy rows.
// Its existing bounded reverse date index therefore finds all copies without
// guessing the user's previous local zone or scanning the Today directory.
func (tx *Tx) chronicleFollowUpDates(summary chronicle.Summary) ([]string, error) {
	if summary.FollowUp == nil {
		return nil, nil
	}
	raw, found, err := tx.readIndexValue(todaySourceNamespace, summary.FollowUp.SourceID, 2048)
	if err != nil || !found {
		return nil, err
	}
	defer clear(raw)
	var dates []string
	if json.Unmarshal(raw, &dates) != nil || len(dates) > maxTodaySourceDates {
		return nil, ErrCorrupt
	}
	for i, date := range dates {
		if !validTodayDate(date) || i > 0 && dates[i-1] >= date {
			return nil, ErrCorrupt
		}
	}
	return dates, nil
}

func (tx *Tx) readChronicleFollowUpSnapshot(date string) (today.Snapshot, bool, error) {
	var snapshot today.Snapshot
	raw, found, err := tx.readIndexValue(todayNamespace, date, today.MaxSnapshotBytes)
	if err != nil || !found {
		return snapshot, found, err
	}
	defer clear(raw)
	if json.Unmarshal(raw, &snapshot) != nil || snapshot.Date != date || snapshot.Validate() != nil {
		return today.Snapshot{}, false, ErrCorrupt
	}
	return snapshot, true, nil
}

func chronicleFollowUpSourcesFit(snapshot today.Snapshot, dependencies []string) bool {
	count := len(snapshot.SourceIDs)
	for _, id := range dependencies {
		if !hasSource(snapshot.SourceIDs, id) {
			count++
		}
	}
	return count <= today.MaxSources
}

func addChronicleFollowUpSources(snapshot *today.Snapshot, dependencies []string) error {
	if !chronicleFollowUpSourcesFit(*snapshot, dependencies) {
		return &storage.StoreError{Code: storage.Invalid}
	}
	for _, id := range dependencies {
		if !hasSource(snapshot.SourceIDs, id) {
			snapshot.SourceIDs = append(snapshot.SourceIDs, id)
			sort.Strings(snapshot.SourceIDs)
		}
	}
	return nil
}

func (tx *Tx) removeChronicleFollowUp(summary chronicle.Summary) error {
	if summary.FollowUp == nil {
		return nil
	}
	dates, err := tx.chronicleFollowUpDates(summary)
	if err != nil {
		return err
	}
	id := chronicleFollowUpID(summary.HourID, summary.FollowUp.SourceID, summary.FollowUp.Text)
	for _, date := range dates {
		snapshot, found, err := tx.readChronicleFollowUpSnapshot(date)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		before := len(snapshot.Questions)
		questions := snapshot.Questions[:0]
		for _, q := range snapshot.Questions {
			if q.ID != id {
				questions = append(questions, q)
			}
		}
		if len(questions) == before {
			continue
		}
		clear(snapshot.Questions[len(questions):])
		snapshot.Questions = questions
		// This is a removal-only projection change: no new text, source, task or
		// answer is admitted. Keep the source directory metadata, which may also
		// cover omitted evidence. Revalidating/hydrating every unrelated source or
		// rewriting its reverse links would multiply this bounded cleanup by up to
		// 512 sources per date. Raw authored answer records are never removed here.
		if snapshot.Validate() != nil {
			return ErrCorrupt
		}
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			return ErrCorrupt
		}
		err = tx.PutIndexRecord(todayNamespace, date, encoded)
		clear(encoded)
		if err != nil {
			return err
		}
	}
	return nil
}

// Observation admission validates the same small encrypted order entry twice:
// once with the primary text and once when reading its captured-at provenance.
// Reuse only the last such entry during the read-only admission phase. The
// caller still authenticates/decodes it on every use. No source text is cached,
// no write occurs through this reader, and the cache never grows with inputs.
type chronicleAdmissionReader struct {
	bolt.Reader
	key, value []byte
}

func (r *chronicleAdmissionReader) Get(key []byte) ([]byte, bool, error) {
	if len(key) < 2 || key[0] != groupOrder || key[1] != kindObservation {
		return r.Reader.Get(key)
	}
	if bytes.Equal(r.key, key) {
		return append([]byte(nil), r.value...), true, nil
	}
	clear(r.key)
	clear(r.value)
	r.key, r.value = nil, nil
	value, found, err := r.Reader.Get(key)
	if err == nil && found && len(value) <= 1024 {
		r.key = append([]byte(nil), key...)
		r.value = append([]byte(nil), value...)
	}
	return value, found, err
}
func (r *chronicleAdmissionReader) close() {
	clear(r.key)
	clear(r.value)
	r.key, r.value = nil, nil
}

// migrateChronicleFollowUpPageLocked repairs copies whose historical recap
// pointer has already been changed or removed. Date keys are opaque HMACs, so
// the one-time migration walks encrypted extension records in physical order.
// It never scans observations, emits plaintext, or calls public Store methods.
// The caller holds the exclusive startup gate; all removals and its persisted
// cursor advance commit atomically. A completed marker avoids future scans.
func (s *Store) migrateChronicleFollowUpPageLocked(ctx context.Context, after []byte, advance func(*Tx, []byte, bool) error) (next []byte, complete bool, err error) {
	if ctx == nil || advance == nil || len(after) != 0 && (len(after) != physicalKeyBytes || after[0] != groupRecord || after[1] != kindIndex) {
		return nil, false, &storage.StoreError{Code: storage.Invalid}
	}
	err = s.updateLocked(ctx, func(tx *Tx) error {
		entries, more, err := tx.writer.Scan([]byte{groupRecord, kindIndex}, after, 32, 2<<20)
		if err != nil {
			return mapStorePhysicalError(err)
		}
		// An arbitrary encrypted extension can exceed the normal page budget. Read
		// that one record under the unchanged physical value/transaction ceilings
		// so an empty but nonterminal page cannot repeat forever.
		if len(entries) == 0 && more {
			clearBoltEntries(entries)
			entries, more, err = tx.writer.Scan([]byte{groupRecord, kindIndex}, after, 1, 8<<20)
			if err != nil {
				return mapStorePhysicalError(err)
			}
		}
		defer clearBoltEntries(entries)
		if len(entries) == 0 && more {
			return ErrCorrupt
		}
		next = append([]byte(nil), after...)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(entry.Key) != physicalKeyBytes || entry.Key[0] != groupRecord || entry.Key[1] != kindIndex {
				return ErrCorrupt
			}
			snapshot, found, err := s.chronicleFollowUpCandidate(entry)
			if err != nil {
				return err
			}
			if found {
				changed, err := tx.removeOrphanChronicleQuestions(&snapshot)
				if err != nil {
					return err
				}
				if changed {
					if snapshot.Validate() != nil {
						return ErrCorrupt
					}
					encoded, err := json.Marshal(snapshot)
					if err != nil {
						return ErrCorrupt
					}
					err = tx.PutIndexRecord(todayNamespace, snapshot.Date, encoded)
					clear(encoded)
					if err != nil {
						return err
					}
				}
			}
			clear(next)
			next = append([]byte(nil), entry.Key...)
		}
		complete = !more
		return advance(tx, next, complete)
	})
	if err != nil {
		clear(next)
		return nil, false, err
	}
	return next, complete, nil
}

func (s *Store) chronicleFollowUpCandidate(entry bolt.Entry) (today.Snapshot, bool, error) {
	var snapshot today.Snapshot
	plain, err := s.open(entry.Key, "index:"+todayNamespace, entry.Value)
	if errors.Is(err, recordcodec.ErrAuthentication) {
		return snapshot, false, nil
	}
	if err != nil {
		return snapshot, false, ErrCorrupt
	}
	defer clear(plain)
	if len(plain) < 5 || len(plain) > today.MaxSnapshotBytes+5 || plain[0] != valueVersion || binary.BigEndian.Uint32(plain[1:5]) != uint32(len(plain)-5) || json.Unmarshal(plain[5:], &snapshot) != nil || snapshot.Validate() != nil {
		return today.Snapshot{}, false, ErrCorrupt
	}
	key := s.logicalKey(groupRecord, kindIndex, "index:"+todayNamespace, snapshot.Date)
	defer clear(key)
	if !bytes.Equal(key, entry.Key) {
		return today.Snapshot{}, false, ErrCorrupt
	}
	return snapshot, true, nil
}

func (tx *Tx) removeOrphanChronicleQuestions(snapshot *today.Snapshot) (bool, error) {
	before := len(snapshot.Questions)
	questions := snapshot.Questions[:0]
	for _, q := range snapshot.Questions {
		hourID, sourceID, generated := generatedChronicleQuestion(q)
		keep := true
		if generated {
			raw, found, err := tx.readIndexValue(chronicleNS, hourID, chronicle.MaxSummaryBytes+(16<<10))
			if err != nil {
				return false, err
			}
			var recap chronicle.Summary
			if found && (json.Unmarshal(raw, &recap) != nil || !validChronicle(recap) || recap.HourID != hourID) {
				clear(raw)
				return false, ErrCorrupt
			}
			clear(raw)
			// A digest is not an input allow-list. Retain only a question whose
			// current recap still proves this exact generated pointer and every
			// actual model dependency. An answer stays independently user-authored.
			keep = found && len(recap.InputSourceIDs) > 0 && recap.FollowUp != nil && recap.FollowUp.SourceID == sourceID && recap.FollowUp.Text == q.Text && slices.Equal(q.SourceIDs, recap.DependencyIDs())
		}
		if keep {
			questions = append(questions, q)
		}
	}
	clear(snapshot.Questions[len(questions):])
	snapshot.Questions = questions
	return len(questions) != before, nil
}

func generatedChronicleQuestion(q today.Question) (hourID, sourceID string, generated bool) {
	if len(q.ID) != len("chronicle-question-")+64 || q.CreatedAt.IsZero() {
		return "", "", false
	}
	hourID = "hour:" + q.CreatedAt.UTC().Truncate(time.Hour).Format("20060102T15Z")
	for _, id := range q.SourceIDs {
		if validHash(id) && q.ID == chronicleFollowUpID(hourID, id, q.Text) {
			return hourID, id, true
		}
	}
	return "", "", false
}
