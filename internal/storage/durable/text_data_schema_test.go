package durable

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

func TestTextDataSchemaEightUpgradePreservesHistoryAndRollsBackAtomically(t *testing.T) {
	path, key := durableTestPath(t), durableTestKey(t, 0x89)
	fixture := writeProcessingSchemaVersion(t, path, key, 8)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	payloads := processingSchemaPayloads(t, path)
	opened, err := openDurableWithHook(path, key, OpenExisting, func() error { return context.Canceled })
	if opened != nil {
		_ = opened.Close()
		t.Fatal("older reader floor was not upgraded atomically")
	}
	if !errors.Is(err, ErrMigration) {
		t.Fatal("interrupted schema8 upgrade did not fail closed", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed migration changed retained memory bytes", err)
	}
	s, err := OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	assertProcessingSchemaHistory(t, s, fixture)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(processingSchemaPayloads(t, path), payloads) {
		t.Fatal("schema8 migration rewrote historical payload ciphertext")
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	assertProcessingSchemaHistory(t, s, fixture)
}

func TestTextDataSchemaRejectsAuthenticatedEightReaderBeforeMutations(t *testing.T) {
	path, key := durableTestPath(t), durableTestKey(t, 0x8a)
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
	state, err := newStoreState(key, false)
	if err != nil {
		t.Fatal(err)
	}
	state.db, err = bolt.OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	old := &Store{state: state}
	defer old.Close()
	if err := old.readIdentityWithSchemaLimit(context.Background(), 8); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatal("schema8 reader admitted new source-linked text metadata", err)
	}
	if state.schemaVer != 0 || state.storeID != ([16]byte{}) {
		t.Fatal("older reader adopted identity before rejecting it")
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("older reader rejection mutated memory", err)
	}
}

func TestPagedLibrarySchemaRejectsAuthenticatedNineReaderBeforeMutations(t *testing.T) {
	path, key := durableTestPath(t), durableTestKey(t, 0x8b)
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
	state, err := newStoreState(key, false)
	if err != nil {
		t.Fatal(err)
	}
	state.db, err = bolt.OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	old := &Store{state: state}
	defer old.Close()
	if err := old.readIdentityWithSchemaLimit(context.Background(), 9); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatal("schema-9 reader admitted paged Library records", err)
	}
	if state.schemaVer != 0 || state.storeID != ([16]byte{}) {
		t.Fatal("older reader adopted identity before rejecting it")
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("older reader rejection mutated memory", err)
	}
}
