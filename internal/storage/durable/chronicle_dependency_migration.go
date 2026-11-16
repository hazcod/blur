package durable

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const (
	chronicleDependencyMigrationNS = "storage-meta"
	chronicleDependencyMigrationID = "chronicle-input-deps-v1"
)

const (
	chronicleDependencyRecaps byte = iota
	chronicleDependencyQuestions
	chronicleDependencyComplete
)

type chronicleDependencyProgress struct {
	phase      byte
	afterHour  string
	afterIndex []byte
}

// Old recaps may have used inputs absent from their displayed citation subset.
// Their dependencies cannot be recovered from a digest or guessed from time.
// Remove only these generated artifacts before publishing the store. Original
// captured text and authored notes remain available for normal recap recovery.
//
// A second pass removes verified generated Today questions that old replacement
// or deletion code left behind without a recap pointer. Both passes commit their
// removal and progress together. A failed Open returns no store; the next Open
// resumes after the committed prefix. Authored answers remain raw saved notes.
func (s *Store) ensureChronicleDependencies(ctx context.Context, afterStep func() error) error {
	raw, found, err := s.readIndexValue(ctx, chronicleDependencyMigrationNS, chronicleDependencyMigrationID, 64)
	if err != nil {
		return err
	}
	progress := chronicleDependencyProgress{}
	if found {
		progress, err = decodeChronicleDependencyMigration(raw)
	}
	clear(raw)
	if err != nil || progress.phase == chronicleDependencyComplete {
		return err
	}
	state := s.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.db == nil {
		return ErrUnavailable
	}
	if state.deletionPending.Load() {
		return ErrDeletionPending
	}
	if err := s.chronicleDependencyViewLocked(ctx, func(reader bolt.Reader) error {
		raw, found, err := s.readIndexValueReader(reader, chronicleDependencyMigrationNS, chronicleDependencyMigrationID, 64)
		if err != nil {
			return err
		}
		defer clear(raw)
		if found {
			clear(progress.afterIndex)
			progress, err = decodeChronicleDependencyMigration(raw)
		}
		return err
	}); err != nil || progress.phase == chronicleDependencyComplete {
		return err
	}
	defer func() { clear(progress.afterIndex) }()
	if progress.phase == chronicleDependencyRecaps {
		if err := s.migrateChronicleDependenciesLocked(ctx, progress.afterHour, afterStep); err != nil {
			return err
		}
		progress = chronicleDependencyProgress{phase: chronicleDependencyQuestions}
		if err := s.updateLocked(ctx, func(tx *Tx) error { return tx.putChronicleDependencyProgress(progress) }); err != nil {
			return err
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		next, complete, err := s.migrateChronicleFollowUpPageLocked(ctx, progress.afterIndex, func(tx *Tx, next []byte, complete bool) error {
			if complete {
				return tx.putChronicleDependencyProgress(chronicleDependencyProgress{phase: chronicleDependencyComplete})
			}
			if !validChronicleDependencyIndexCursor(next) || bytes.Compare(next, progress.afterIndex) <= 0 {
				return ErrCorrupt
			}
			return tx.putChronicleDependencyProgress(chronicleDependencyProgress{phase: chronicleDependencyQuestions, afterIndex: next})
		})
		if err != nil || complete {
			clear(next)
			return err
		}
		if !validChronicleDependencyIndexCursor(next) || bytes.Compare(next, progress.afterIndex) <= 0 {
			clear(next)
			return ErrCorrupt
		}
		clear(progress.afterIndex)
		progress.afterIndex = next
	}
}

// The authenticated day directory is loaded once (at most 200 KiB), each day's
// list has at most 24 hours, and each transaction inspects/removes one recap.
// The private hook covers recap commits, retaining its existing test contract.
func (s *Store) migrateChronicleDependenciesLocked(ctx context.Context, after string, afterStep func() error) error {
	var days []string
	if err := s.chronicleDependencyViewLocked(ctx, func(reader bolt.Reader) error {
		var err error
		days, err = s.readChronicleDays(reader)
		return err
	}); err != nil {
		return err
	}
	firstDay := 0
	if after != "" {
		at, _ := time.Parse("hour:20060102T15Z", after) // decoder checked canonical form
		firstDay = sort.SearchStrings(days, at.Format("2006-01-02"))
	}
	for _, day := range days[firstDay:] {
		var ids []string
		if err := s.chronicleDependencyViewLocked(ctx, func(reader bolt.Reader) error {
			var err error
			ids, err = s.readChronicleDay(reader, day)
			if err == nil && len(ids) == 0 {
				return ErrCorrupt
			}
			return err
		}); err != nil {
			return err
		}
		for _, id := range ids {
			if id <= after {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := s.updateLocked(ctx, func(tx *Tx) error {
				raw, found, err := tx.readIndexValue(chronicleNS, id, chronicle.MaxSummaryBytes+(16<<10))
				if err != nil {
					return err
				}
				defer clear(raw)
				var recap chronicle.Summary
				if !found || json.Unmarshal(raw, &recap) != nil || !validChronicle(recap) || recap.HourID != id || recap.Start.Format("2006-01-02") != day {
					return ErrCorrupt
				}
				if len(recap.InputSourceIDs) == 0 {
					if err := tx.deleteChronicle(id); err != nil {
						return err
					}
				}
				return tx.putChronicleDependencyProgress(chronicleDependencyProgress{phase: chronicleDependencyRecaps, afterHour: id})
			}); err != nil {
				return err
			}
			after = id
			if afterStep != nil {
				if err := afterStep(); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// The exclusive startup gate is held across these bounded views and writes.
func (s *Store) chronicleDependencyViewLocked(ctx context.Context, read func(bolt.Reader) error) error {
	var cause error
	err := s.state.db.View(ctx, func(reader bolt.Reader) error {
		cause = read(reader)
		if cause != nil {
			return errAbortTransaction
		}
		return nil
	})
	if cause != nil {
		return cause
	}
	return mapStorePhysicalError(err)
}

func decodeChronicleDependencyMigration(raw []byte) (chronicleDependencyProgress, error) {
	invalid := chronicleDependencyProgress{}
	if len(raw) < 2 || len(raw) > 64 {
		return invalid, ErrCorrupt
	}
	if raw[0] == 1 {
		// Earlier builds completed only the recap pass. Their authenticated
		// completion marker still needs the orphan-question pass exactly once.
		if raw[1] > 1 || raw[1] == 1 && len(raw) != 2 {
			return invalid, ErrCorrupt
		}
		if raw[1] == 1 {
			return chronicleDependencyProgress{phase: chronicleDependencyQuestions}, nil
		}
		after := string(raw[2:])
		if after != "" && !validChronicleHourID(after) {
			return invalid, ErrCorrupt
		}
		return chronicleDependencyProgress{phase: chronicleDependencyRecaps, afterHour: after}, nil
	}
	if raw[0] != 2 {
		return invalid, ErrCorrupt
	}
	progress := chronicleDependencyProgress{phase: raw[1]}
	switch progress.phase {
	case chronicleDependencyRecaps:
		progress.afterHour = string(raw[2:])
		if progress.afterHour != "" && !validChronicleHourID(progress.afterHour) {
			return invalid, ErrCorrupt
		}
	case chronicleDependencyQuestions:
		if len(raw) != 2 && !validChronicleDependencyIndexCursor(raw[2:]) {
			return invalid, ErrCorrupt
		}
		progress.afterIndex = append([]byte(nil), raw[2:]...)
	case chronicleDependencyComplete:
		if len(raw) != 2 {
			return invalid, ErrCorrupt
		}
	default:
		return invalid, ErrCorrupt
	}
	return progress, nil
}

func encodeChronicleDependencyMigration(after string, complete bool) []byte {
	if complete {
		return []byte{2, chronicleDependencyComplete}
	}
	return append([]byte{2, chronicleDependencyRecaps}, after...)
}

func validChronicleDependencyIndexCursor(cursor []byte) bool {
	return len(cursor) == physicalKeyBytes && cursor[0] == groupRecord && cursor[1] == kindIndex
}

func (tx *Tx) putChronicleDependencyProgress(progress chronicleDependencyProgress) error {
	raw := []byte{2, progress.phase}
	switch progress.phase {
	case chronicleDependencyRecaps:
		raw = append(raw, progress.afterHour...)
	case chronicleDependencyQuestions:
		raw = append(raw, progress.afterIndex...)
	}
	defer clear(raw)
	decoded, err := decodeChronicleDependencyMigration(raw)
	clear(decoded.afterIndex)
	if err != nil {
		return err
	}
	return tx.PutIndexRecord(chronicleDependencyMigrationNS, chronicleDependencyMigrationID, raw)
}
