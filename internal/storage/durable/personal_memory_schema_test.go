package durable

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"testing"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

func TestPermanentDeletionFencesRequireSchemaFive(t *testing.T) {
	// Older clients do not know how to delete repeated-visit metadata or
	// source-linked personal-memory projections. They must reject this store at identity.
	if OpenSchemaVersion < 5 {
		t.Fatalf("personal-memory schema = %d, want at least 5", OpenSchemaVersion)
	}
}

func TestSchemaTwoToCurrentIsAtomicPreservesEvidenceAndReopens(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xe7)
	writeSchemaVersionFixture(t, path, key, 2)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openDurableWithHook(path, key, OpenExisting, func() error { return errors.New("interrupted current-schema upgrade") }); !errors.Is(err, ErrMigration) {
		t.Fatal("schema2 did not run the personal-memory migration", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("interrupted current-schema migration changed durable bytes", err)
	}
	s, err := OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if s.state.schemaVer != OpenSchemaVersion {
		t.Fatal("store did not upgrade", s.state.schemaVer)
	}
	old := schemaMigrationFixtureObservation(t)
	if got, found, err := s.GetObservation(ctx, old.ContentID()); err != nil || !found || got.Text() != old.Text() {
		t.Fatal("old encrypted evidence changed", found, err)
	}
	newSource, _ := capture.SanitizeObservation("new.app", "local", "new schema evidence", 512)
	if err := s.PutObservation(ctx, newSource); err != nil {
		t.Fatal(err)
	}
	if err := s.view(ctx, func(reader bolt.Reader) error {
		identity, found, err := reader.Get(identityKey)
		if err != nil || !found {
			t.Fatal(found, err)
		}
		defer clear(identity)
		if binary.BigEndian.Uint32(identity[17:identityHeader]) != OpenSchemaVersion {
			t.Fatal("legacy reader would accept the identity version")
		}
		key := s.logicalKey(groupRecord, kindObservation, "", newSource.ContentID())
		defer clear(key)
		record, found, err := reader.Get(key)
		if err != nil || !found {
			t.Fatal(found, err)
		}
		defer clear(record)
		if binary.BigEndian.Uint32(record[1:storedValueHeader]) != OpenSchemaVersion {
			t.Fatal("new record used an old authenticated schema")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetObservation(ctx, newSource.ContentID()); err != nil || !found {
		t.Fatal("current-schema reopen lost evidence", found, err)
	}
}
