package durable

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"testing"

	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

func TestCaptureRecoveryRequiresSchemaSix(t *testing.T) {
	if OpenSchemaVersion < 6 {
		t.Fatalf("capture schema = %d, requires at least 6", OpenSchemaVersion)
	}
}

func TestCaptureIndexReadyReopenPreservesDurableBytes(t *testing.T) {
	path, key := durableTestPath(t), durableTestKey(t, 0x9a)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("ready capture index caused a write on reopen", err)
	}
}

func TestCaptureIndexReadyReopenRejectsInvalidMetadata(t *testing.T) {
	for _, component := range []string{"index state", "backfill cursor", "backfill content format"} {
		t.Run(component, func(t *testing.T) {
			ctx := context.Background()
			path, key := durableTestPath(t), durableTestKey(t, 0x9b)
			s, err := OpenDurable(path, key, CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.captureUpdate(ctx, func(tx *Tx) error {
				if component == "index state" {
					index := tx.capturePartSearch()
					return index.putSearchIndexValueLocked(index.store.exactStateKey(), "search-index-state-v1", []byte{searchIndexFormatVersion, 0}, true)
				}
				if component == "backfill content format" {
					return tx.capturePutJSONLocked(captureIndexBackfillKey, captureIndexBackfill{Version: captureIndexVersion, Format: captureIndexContentFormat + 1}, 256)
				}
				return tx.capturePutJSONLocked(captureIndexBackfillKey, captureIndexBackfill{Version: captureIndexVersion + 1}, 256)
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			opened, err := OpenDurable(path, key, OpenExisting)
			if opened != nil {
				_ = opened.Close()
				t.Fatal("invalid capture index metadata published an open store")
			}
			if !errors.Is(err, ErrCorrupt) {
				t.Fatal("invalid capture index metadata did not fail closed", err)
			}
		})
	}
}

func TestCaptureSchemaFiveWriterRejectsAuthenticatedSixIdentity(t *testing.T) {
	s := openReplicaTestStore(t)
	if err := s.readIdentityWithSchemaLimit(context.Background(), 5); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatal("schema5 reader accepted schema6 identity", err)
	}
	if s.state.schemaVer != OpenSchemaVersion {
		t.Fatal("rejected older reader mutated the active schema")
	}
}

func TestCaptureSchemaFiveMigrationIsAtomicAndPreservesPrimaryBytes(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0x99)
	writeSchemaVersionFixture(t, path, key, 5)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openDurableWithHook(path, key, OpenExisting, func() error { return context.Canceled }); !errors.Is(err, ErrMigration) {
		t.Fatal("schema5 did not run the required atomic upgrade", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed schema6 upgrade rewrote durable data", err)
	}
	var original []byte
	db, err := bolt.OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.View(ctx, func(r bolt.Reader) error {
		rows, more, err := r.Scan(observationRecordPrefix, nil, 2, 1<<20)
		defer clearBoltEntries(rows)
		if err != nil {
			return err
		}
		if more || len(rows) != 1 {
			t.Fatal("unexpected historical primary fixture")
		}
		original = bytes.Clone(rows[0].Value)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	want := schemaMigrationFixtureObservation(t)
	if got, found, err := s.GetObservation(ctx, want.ContentID()); err != nil || !found || got.Text() != want.Text() {
		t.Fatal("historical observation no longer readable", found, err)
	}
	if err := s.view(ctx, func(r bolt.Reader) error {
		primaryKey := s.logicalKey(groupRecord, kindObservation, "", want.ContentID())
		defer clear(primaryKey)
		value, found, err := r.Get(primaryKey)
		defer clear(value)
		if err != nil {
			return err
		}
		if !found || !bytes.Equal(original, value) {
			t.Fatal("migration rewrote historical evidence")
		}
		identity, found, err := r.Get(identityKey)
		defer clear(identity)
		if err != nil {
			return err
		}
		if !found || binary.BigEndian.Uint32(identity[17:identityHeader]) != OpenSchemaVersion {
			t.Fatal("schema5 writer would accept migrated identity")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
