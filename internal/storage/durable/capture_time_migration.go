package durable

import (
	"context"
	"encoding/binary"
	"time"

	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const (
	captureTimeMigrationNamespace = "storage-meta"
	captureTimeMigrationID        = "capture-time-baseline-v1"
)

// IsObservationTimeKnown distinguishes observed times from the conservative
// retention baseline assigned by old migrations. Exact collisions are
// indistinguishable and conservatively remain unknown. Original metadata is
// retained for retention/index compatibility; later visits remain observed.
func (s *Store) IsObservationTimeKnown(at time.Time) bool {
	return s != nil && s.state != nil && !at.IsZero() && !at.Equal(s.state.captureTimeBaseline)
}

// ObservationCaptureTime reads only authenticated order metadata. It avoids
// decrypting the original text twice when a caller already hydrated it.
func (s *Store) ObservationCaptureTime(ctx context.Context, id string) (time.Time, error) {
	if ctx == nil || !validHash(id) {
		return time.Time{}, &storage.StoreError{Code: storage.Invalid}
	}
	var at time.Time
	err := s.view(ctx, func(reader bolt.Reader) error {
		var err error
		at, err = s.observationCapturedAtReader(reader, id)
		return err
	})
	return at, err
}

func (s *Store) ensureCaptureTimeMetadata(ctx context.Context) error {
	baseline, complete, found, err := s.readCaptureTimeMigration(ctx)
	if err != nil {
		return err
	}
	if !found {
		baseline = time.Now().UTC()
		if !validCaptureTime(baseline) {
			return ErrMigration
		}
		if err := s.Update(ctx, func(tx *Tx) error {
			return tx.PutIndexRecord(captureTimeMigrationNamespace, captureTimeMigrationID, encodeCaptureTimeMigration(baseline, false))
		}); err != nil {
			return ErrMigration
		}
	}
	if complete {
		return nil
	}

	var after []byte
	for {
		more := false
		err := s.Update(ctx, func(tx *Tx) error {
			entries, hasMore, err := tx.writer.Scan(observationOrderPrefix, after, storage.MaxObservationPageRows, bolt.MaxTransactionBytes-bolt.MaxKeyBytes)
			if err != nil {
				return mapStorePhysicalError(err)
			}
			defer clearBoltEntries(entries)
			if len(entries) == 0 && hasMore {
				return ErrCorrupt
			}
			if len(entries) > 0 {
				after = append(after[:0], entries[len(entries)-1].Key...)
			}
			for _, entry := range entries {
				size, capturedAt, recordKey, err := s.decodeSizeEntry(entry.Key, entry.Value)
				if err != nil {
					return err
				}
				if !capturedAt.IsZero() {
					clear(recordKey)
					continue
				}
				envelope, err := s.encodeSizeEntry(entry.Key, recordKey, size, baseline)
				clear(recordKey)
				if err != nil {
					return err
				}
				tx.mu.Lock()
				err = tx.writeEntryLocked(entry.Key, envelope, true)
				tx.mu.Unlock()
				clear(envelope)
				if err != nil {
					return err
				}
			}
			more = hasMore
			if !hasMore {
				return tx.PutIndexRecord(captureTimeMigrationNamespace, captureTimeMigrationID, encodeCaptureTimeMigration(baseline, true))
			}
			return nil
		})
		if err != nil {
			return ErrMigration
		}
		if !more {
			return nil
		}
	}
}

func (s *Store) readCaptureTimeMigration(ctx context.Context) (time.Time, bool, bool, error) {
	encoded, found, err := s.readIndexValue(ctx, captureTimeMigrationNamespace, captureTimeMigrationID, 16)
	if err != nil || !found {
		return time.Time{}, false, found, err
	}
	defer clear(encoded)
	if len(encoded) != 10 || encoded[0] != 1 || encoded[1] > 1 {
		return time.Time{}, false, false, ErrCorrupt
	}
	baseline := time.Unix(0, int64(binary.BigEndian.Uint64(encoded[2:10]))).UTC()
	if !validCaptureTime(baseline) {
		return time.Time{}, false, false, ErrCorrupt
	}
	return baseline, encoded[1] == 1, true, nil
}

func encodeCaptureTimeMigration(baseline time.Time, complete bool) []byte {
	encoded := make([]byte, 10)
	encoded[0] = 1
	if complete {
		encoded[1] = 1
	}
	binary.BigEndian.PutUint64(encoded[2:], uint64(baseline.UTC().UnixNano()))
	return encoded
}
