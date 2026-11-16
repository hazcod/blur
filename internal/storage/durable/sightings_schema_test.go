package durable

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

// Build a fully authenticated schema-3 fixture, including index values and a
// phase-7 journal. Every value is resealed under its historical AEAD context;
// changing only the identity would not exercise old record compatibility.
func writeSchemaThreeErasureFixture(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	key := durableTestKey(t, 0x91)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	o := schemaMigrationFixtureObservation(t)
	if err := s.PutObservation(ctx, o); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, func(tx *Tx) error {
		if err := tx.deleteKnowledgeRecord(captureTimeMigrationNamespace, deletionMigrationID); err != nil {
			return err
		}
		if err := tx.deleteKnowledgeRecord(deletionCutoffNS, deletionSequenceID); err != nil {
			return err
		}
		return tx.PutIndexRecord(erasureNamespace, erasureJournalID, mustJSON(sourceErasure{SourceID: o.ContentID(), Phase: 7}))
	}); err != nil {
		t.Fatal(err)
	}
	err = s.state.db.Update(ctx, func(writer bolt.Writer) error {
		tx := &Tx{store: s, writer: writer, active: true}
		if err := tx.loadManifestLocked(); err != nil {
			return err
		}
		entries, more, err := writer.Scan(nil, nil, 128, bolt.MaxTransactionBytes-bolt.MaxKeyBytes)
		if err != nil {
			return err
		}
		if more {
			t.Fatal("schema fixture exceeded its record bound")
		}
		defer clearBoltEntries(entries)
		for _, entry := range entries {
			if bytes.Equal(entry.Key, manifestKey) {
				continue
			}
			replacement, err := schemaFixtureRecordAtVersion(t, s, entry, 3)
			if err != nil {
				return err
			}
			err = tx.writeEntryLocked(entry.Key, replacement, true)
			clear(replacement)
			if err != nil {
				return err
			}
		}
		s.state.schemaVer = 3
		return tx.flushManifestLocked()
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSchemaThreeUpgradeRollsBackAndResumesOldPhaseSevenErasure(t *testing.T) {
	path := durableTestPath(t)
	key := durableTestKey(t, 0x91)
	writeSchemaThreeErasureFixture(t, path)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openDurableWithHook(path, key, OpenExisting, func() error { return errors.New("interrupt upgrade") }); !errors.Is(err, ErrMigration) {
		t.Fatal("schema3 upgrade did not use atomic migration", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("interrupted schema3 upgrade changed durable bytes", err)
	}
	s, err := OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	if s.state.schemaVer != OpenSchemaVersion {
		t.Fatal("schema3 identity was not upgraded", s.state.schemaVer)
	}
	if _, found, err := s.GetObservation(context.Background(), schemaMigrationFixtureObservation(t).ContentID()); err != nil || found {
		t.Fatal("old phase7 journal did not resume before publishing store", found, err)
	}
}
