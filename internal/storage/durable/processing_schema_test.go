package durable

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/privacy"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"github.com/hazcod/blur/internal/tasks"
	"github.com/hazcod/blur/internal/today"
)

type processingSchemaFixture struct {
	source, note capture.SanitizedObservation
	capture      capture.CaptureSummary
	captureText  string
	task         tasks.Item
	derivation   storage.Derivation
	generation   storage.Generation
	today        today.Snapshot
}

// Write through a historical authenticated context instead of downgrading a
// published current store. This keeps every historical payload at its original
// schema while exercising actual source, user-memory and derived-data APIs.
func writeProcessingSchemaSeven(t *testing.T, path string, key securestore.Key) processingSchemaFixture {
	t.Helper()
	return writeProcessingSchemaVersion(t, path, key, 7)
}

func writeProcessingSchemaVersion(t *testing.T, path string, key securestore.Key, version uint32) processingSchemaFixture {
	t.Helper()
	ctx := context.Background()
	writeSchemaVersionFixture(t, path, key, version)
	state, err := newStoreState(key, false)
	if err != nil {
		t.Fatal(err)
	}
	state.db, err = bolt.OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{state: state}
	defer func() { _ = s.Close() }()
	if err := s.readIdentityWithSchemaLimit(ctx, version); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	fixture := processingSchemaFixture{source: schemaMigrationFixtureObservation(t)}
	fixture.note, err = capture.SanitizeObservation("sh.blur.notes", "local", "Historical authored note: the accepted launch date is Friday.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveNote(ctx, fixture.note, at); err != nil {
		t.Fatal(err)
	}
	fixture.today, _, err = s.GetTodaySnapshot(ctx, "2026-11-12")
	if err != nil {
		t.Fatal(err)
	}
	fixture.captureText = strings.Repeat("Historical terminal evidence remains ordered and complete.\n", 160) + "TAIL: customer accepted the revised owner."
	h, parts, summary := makeStoredCapture(t, fixture.captureText, at.Add(time.Hour))
	stageStoredCapture(t, s, h, parts)
	if _, err := s.PublishCapture(ctx, summary); err != nil {
		t.Fatal(err)
	}
	fixture.capture = summary
	fixture.task = taskFixture(0x87)
	fixture.task.UserAuthored, fixture.task.SourceID = false, fixture.source.ContentID()
	if err := s.PutTask(ctx, fixture.task, 0); err != nil {
		t.Fatal(err)
	}
	fixture.task, _, err = s.GetTask(ctx, fixture.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.derivation = storage.Derivation{
		ID: "schema-seven-derived-history", GenerationID: "schema-seven-generation", State: storage.Active,
		SourceIDs: []string{fixture.source.ContentID()}, SourceHashes: []string{fixture.source.ContentID()},
		ProcessorVersion: 1, SchemaVersion: 1, PromptVersion: 1, RedactionVersion: privacy.Version,
		ProviderModelDigest: fmt.Sprintf("%064x", 2), OutputHash: fmt.Sprintf("%064x", 3), CoveredSources: 1,
	}
	fixture.generation = storage.Generation{ID: fixture.derivation.GenerationID, DerivationIDs: []string{fixture.derivation.ID}, SchemaVersion: 1, RedactionVersion: privacy.Version}
	if err := s.PutDerivation(ctx, fixture.derivation); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGeneration(ctx, fixture.generation); err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateGeneration(ctx, fixture.generation.ID); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func processingSchemaPayloads(t *testing.T, path string) map[string][]byte {
	t.Helper()
	db, err := bolt.OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	payloads := make(map[string][]byte)
	if err := db.View(context.Background(), func(reader bolt.Reader) error {
		var after []byte
		for {
			entries, more, err := reader.Scan(nil, after, 128, bolt.MaxTransactionBytes-bolt.MaxKeyBytes)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if !bytes.Equal(entry.Key, identityKey) && !bytes.Equal(entry.Key, schemaKey) && !bytes.Equal(entry.Key, manifestKey) {
					payloads[string(entry.Key)] = bytes.Clone(entry.Value)
				}
			}
			if !more {
				clearBoltEntries(entries)
				return nil
			}
			after = bytes.Clone(entries[len(entries)-1].Key)
			clearBoltEntries(entries)
		}
	}); err != nil {
		t.Fatal(err)
	}
	return payloads
}

func assertProcessingSchemaHistory(t *testing.T, s *Store, fixture processingSchemaFixture) {
	t.Helper()
	ctx := context.Background()
	for _, source := range []capture.SanitizedObservation{fixture.source, fixture.note} {
		got, found, err := s.GetObservation(ctx, source.ContentID())
		if err != nil || !found || got.Text() != source.Text() {
			t.Fatal("schema upgrade lost historical source", source.ContentID(), found, err)
		}
	}
	if got, found, err := s.GetTodaySnapshot(ctx, "2026-11-12"); err != nil || !found || !reflect.DeepEqual(got, fixture.today) {
		t.Fatal("schema upgrade changed authored note history", got, found, err)
	}
	if got, found, err := s.GetTask(ctx, fixture.task.ID); err != nil || !found || !reflect.DeepEqual(got, fixture.task) {
		t.Fatal("schema upgrade changed task or source links", got, found, err)
	}
	if got, found, err := s.GetDerivation(ctx, fixture.derivation.ID); err != nil || !found || !reflect.DeepEqual(got, fixture.derivation) {
		t.Fatal("schema upgrade changed derived history", got, found, err)
	}
	if got, found, err := s.ActiveGeneration(ctx); err != nil || !found || !reflect.DeepEqual(got, fixture.generation) {
		t.Fatal("schema upgrade changed active generation", got, found, err)
	}
	got, found, err := s.GetCapture(ctx, fixture.capture.ContentID())
	if err != nil || !found || got.Summary.ContentID() != fixture.capture.ContentID() || got.Visits != 1 {
		t.Fatal("schema upgrade changed capture history", got, found, err)
	}
	var text strings.Builder
	for after := int64(-1); ; {
		page, err := s.ReadCaptureParts(ctx, fixture.capture.ContentID(), after, 8)
		if err != nil {
			t.Fatal(err)
		}
		for _, part := range page.Parts {
			text.WriteString(part.Text())
		}
		if !page.HasMore {
			break
		}
		after = page.Next
	}
	if text.String() != fixture.captureText {
		t.Fatal("schema upgrade changed ordered capture payloads")
	}
}

func TestProcessingSchemaSevenUpgradeIsAtomicAndPreservesAllHistory(t *testing.T) {
	path, key := durableTestPath(t), durableTestKey(t, 0x87)
	fixture := writeProcessingSchemaSeven(t, path, key)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	payloads := processingSchemaPayloads(t, path)
	opened, err := openDurableWithHook(path, key, OpenExisting, func() error { return context.Canceled })
	if opened != nil {
		_ = opened.Close()
		t.Fatal("interrupted reader-floor upgrade published a store")
	}
	if !errors.Is(err, ErrMigration) {
		t.Fatal("schema7 did not enter the required atomic reader-floor upgrade", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("interrupted schema8 upgrade changed durable bytes", err)
	}
	s, err := OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	assertProcessingSchemaHistory(t, s, fixture)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := processingSchemaPayloads(t, path); !reflect.DeepEqual(got, payloads) {
		t.Fatal("metadata-only schema upgrade rewrote retained payload ciphertext")
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	assertProcessingSchemaHistory(t, s, fixture)
}

func TestProcessingSchemaRejectsAuthenticatedSevenReaderBeforeDeletion(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0x88)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	id, chunks := sourceCompactionFixture(t, s, "new-reader-required", 1000)
	applyCompactionChunks(t, s, chunks)
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
	// Identity admission precedes every recovery/deletion mutation. A schema7
	// client must stop here: it cannot erase the new compaction/pending/progress
	// records, especially when resuming an older erasure journal mid-phase.
	if err := old.readIdentityWithSchemaLimit(ctx, 7); !errors.Is(err, ErrUnsupportedSchema) {
		_ = old.Close()
		t.Fatal("schema7 reader admitted memory whose derivatives it cannot erase", err)
	}
	if state.schemaVer != 0 || state.storeID != ([16]byte{}) {
		t.Fatal("rejected older reader adopted authenticated store identity")
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("older reader rejection rewrote memory before deletion", err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	page, err := s.ListSourceCompactions(ctx, id, 0, 32)
	if err != nil || len(page.Blocks) != len(chunks) {
		t.Fatal("older reader rejection lost protected derived memory", page, err)
	}
	if err := s.DeleteObservationRecord(ctx, id); err != nil {
		t.Fatal("current reader could not erase its protected source", err)
	}
	assertCompactionSlotsAbsent(t, s, id, len(chunks))
	if _, found, err := s.GetObservation(ctx, id); err != nil || found {
		t.Fatal("current reader left deleted source", found, err)
	}
}
