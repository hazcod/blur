package durable

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

type cutoffReader interface {
	ObservationDeletionCovers(context.Context, string, time.Time) (bool, error)
}

func TestCompletedFenceMigrationRejectsMissingSequenceOrLastPage(t *testing.T) {
	for _, component := range []string{"sequence", "last page"} {
		t.Run(component, func(t *testing.T) {
			ctx := context.Background()
			path, key := durableTestPath(t), durableTestKey(t, 0xa3)
			s, err := OpenDurable(path, key, CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.RecordTombstone(ctx, Tombstone{SourceID: fmt.Sprintf("%064x", 1), At: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if err := s.Update(ctx, func(tx *Tx) error {
				if component == "sequence" {
					return tx.deleteKnowledgeRecord(deletionCutoffNS, deletionSequenceID)
				}
				return tx.deleteKnowledgeRecord(deletionChangeNS, deletionChangePageID(0))
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			opened, err := OpenDurable(path, key, OpenExisting)
			if opened != nil {
				_ = opened.Close()
				t.Fatal("published store with missing deletion component")
			}
			if !errors.Is(err, ErrCorrupt) {
				t.Fatal("missing component did not fail closed", err)
			}
		})
	}
}

func TestSchemaFourFenceMigrationResumesEveryCommittedCheckpoint(t *testing.T) {
	if os.Getenv("BLUR_FENCE_CRASH_HELPER") == "1" {
		stop, err := strconv.Atoi(os.Getenv("BLUR_FENCE_CRASH_STEP"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = openDurableWithHooks(os.Getenv("BLUR_FENCE_CRASH_PATH"), durableTestKey(t, 0xa8), OpenExisting, nil, func(next int) error {
			if next == stop {
				os.Exit(0)
			}
			return nil
		})
		t.Fatal("crash checkpoint was not reached", err)
	}
	for _, stop := range []int{0, 32, 64, 65} {
		t.Run(fmt.Sprint(stop), func(t *testing.T) {
			ctx := context.Background()
			path, key := durableTestPath(t), durableTestKey(t, 0xa8)
			writeSchemaVersionFixture(t, path, key, 4)
			state, err := newStoreState(key, false)
			if err != nil {
				t.Fatal(err)
			}
			state.db, err = bolt.OpenExisting(path)
			if err != nil {
				t.Fatal(err)
			}
			legacyStore := &Store{state: state}
			if err := legacyStore.readIdentity(ctx); err != nil {
				t.Fatal(err)
			}
			at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
			legacy := make([]Tombstone, 65)
			for i := range legacy {
				legacy[i] = Tombstone{SourceID: fmt.Sprintf("%064x", i+1), At: at.Add(time.Duration(i) * time.Second)}
			}
			if err := legacyStore.Update(ctx, func(tx *Tx) error { return tx.PutIndexRecord(tombstoneNS, tombstoneLogID, encodeTombstones(legacy)) }); err != nil {
				t.Fatal(err)
			}
			if err := legacyStore.Close(); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(os.Args[0], "-test.run=^TestSchemaFourFenceMigrationResumesEveryCommittedCheckpoint$")
			command.Env = append(os.Environ(), "BLUR_FENCE_CRASH_HELPER=1", "BLUR_FENCE_CRASH_PATH="+path, "BLUR_FENCE_CRASH_STEP="+strconv.Itoa(stop))
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("crash helper: %v %s", err, output)
			}
			s, err := OpenDurable(path, key, OpenExisting)
			if err != nil {
				t.Fatal(err)
			}
			defer func(close func() error) { _ = close() }(s.Close)
			if s.state.schemaVer != OpenSchemaVersion {
				t.Fatal("old writer could still enter", s.state.schemaVer)
			}
			page, err := s.DeletionChanges(ctx, 0, 256)
			if err != nil || len(page.Changes) != len(legacy) || page.Next != 65 || page.HasMore {
				t.Fatal("restart duplicated or lost cutoffs", page, err)
			}
			for i, change := range page.Changes {
				if change.Tombstone != legacy[i] || change.Sequence != uint64(i+1) {
					t.Fatal("migration changed deletion provenance", change, legacy[i])
				}
			}
		})
	}
}

func TestCutoffBoundariesMonotonicPagesAndNewCapture(t *testing.T) {
	s := openReplicaTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	o, _ := capture.SanitizeObservation("reader.app", "example.com", "Fresh capture at cutoff boundary", 512)
	for _, tomb := range []Tombstone{{SourceID: AllSourcesTombstone, At: at}, {SourceID: o.ContentID(), At: at}, {SourceID: o.ContentID(), At: at.Add(-time.Second)}, {SourceID: o.ContentID(), At: at}} {
		if err := s.RecordTombstone(ctx, tomb); err != nil {
			t.Fatal(err)
		}
	}
	if covered, err := s.ObservationDeletionCovers(ctx, fmt.Sprintf("%064x", 2), at); err != nil || covered {
		t.Fatal("all-before must be strict", covered, err)
	}
	if err := s.PutObservationAt(ctx, o, at); !errors.Is(err, ErrSourceDeleted) {
		t.Fatal("source cutoff must include equality", err)
	}
	if err := s.PutObservationAt(ctx, o, at.Add(time.Nanosecond)); err != nil {
		t.Fatal("genuine newer capture was blocked", err)
	}
	for i := 0; i < 132; i++ {
		if err := s.RecordTombstone(ctx, Tombstone{SourceID: fmt.Sprintf("%064x", i+3), At: at}); err != nil {
			t.Fatal(err)
		}
	}
	var after uint64
	for {
		page, err := s.DeletionChanges(ctx, after, 17)
		if err != nil {
			t.Fatal(err)
		}
		for _, change := range page.Changes {
			after++
			if change.Sequence != after {
				t.Fatal("sequence gap", change)
			}
		}
		if page.Next != after {
			t.Fatal("cursor skipped changes", page.Next, after)
		}
		if !page.HasMore {
			break
		}
	}
	if after != 134 {
		t.Fatal("duplicate or older cutoff appended a change", after)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.DeletionChanges(cancelled, 0, 17); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled read was published", err)
	}
}

func TestSourceAdmissionChecksDeletionAtTheWriteBoundary(t *testing.T) {
	for _, kind := range []string{"capture", "note", "sighting"} {
		t.Run(kind, func(t *testing.T) {
			s := openReplicaTestStore(t)
			ctx := context.Background()
			at := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
			app, domain := "reader.app", "example.com"
			if kind == "note" {
				app, domain = "sh.blur.notes", "local"
			}
			o, _ := capture.SanitizeObservation(app, domain, "Previously deleted Aurora memory", 512)
			if kind == "sighting" {
				if err := s.PutObservationAt(ctx, o, at.Add(-time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.RecordTombstone(ctx, Tombstone{SourceID: o.ContentID(), At: at}); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "capture":
				err = s.PutObservationAt(ctx, o, at.Add(-time.Minute))
			case "note":
				err = s.SaveNote(ctx, o, at.Add(-time.Minute))
			case "sighting":
				err = s.RecordObservationSighting(ctx, o, at.Add(-time.Minute))
			}
			if err == nil {
				t.Fatal("stale source admission ignored a committed deletion")
			}
			if kind != "sighting" {
				if _, found, err := s.GetObservation(ctx, o.ContentID()); err != nil || found {
					t.Fatalf("rejected source persisted: %t %v", found, err)
				}
			}
		})
	}
}

func TestPreparedTodayProjectionCannotRestoreDeletedNote(t *testing.T) {
	s := openReplicaTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	o, _ := capture.SanitizeObservation("sh.blur.notes", "local", "Aurora note prepared before deletion", 512)
	if err := s.SaveNote(ctx, o, at); err != nil {
		t.Fatal(err)
	}
	date, zone := localDate(at)
	prepared, err := s.noteSnapshot(ctx, date, zone, at, o.ContentID(), o.Text())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTombstone(ctx, Tombstone{SourceID: o.ContentID(), At: at.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteObservationRecord(ctx, o.ContentID()); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, func(tx *Tx) error { return tx.putTodaySnapshot(prepared) }); err == nil {
		t.Fatal("prepared Today source survived deletion between prepare and commit")
	}
	page, err := s.ListObservationPage(ctx, storage.ObservationPageRequest{Limit: 8, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Observations) != 0 {
		t.Fatalf("projection write recreated source: %+v %v", page, err)
	}
}

func TestConditionalReplayErasesLegacyOrphansWithoutAPrimary(t *testing.T) {
	s := openReplicaTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	o, _ := capture.SanitizeObservation("sh.blur.notes", "local", "Legacy orphaned private note projection", 512)
	if err := s.SaveNote(ctx, o, at); err != nil {
		t.Fatal(err)
	}
	// Older writers could leave a projection after primary removal. Keep
	// authenticated reverse indexes to exercise the normal erasure cascade.
	if err := s.Update(ctx, func(tx *Tx) error { return tx.deleteObservation(o.ContentID()) }); err != nil {
		t.Fatal(err)
	}
	date, _ := localDate(at)
	if _, found, err := s.GetTodaySnapshot(ctx, date); err != nil || !found {
		t.Fatal("fixture has no legacy orphan", found, err)
	}
	if _, err := s.DeleteObservationRecordBefore(ctx, o.ContentID(), at, true, nil); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetTodaySnapshot(ctx, date); err != nil || found {
		t.Fatal("replayed deletion left legacy orphaned content", found, err)
	}
}

func TestBoundedConditionalDeletionCommitsWithoutStagedJournal(t *testing.T) {
	s := openReplicaTestStore(t)
	ctx := context.Background()
	o, _ := capture.SanitizeObservation("reader.app", "example.com", "A bounded source deletion", 512)
	if err := s.PutObservationAt(ctx, o, time.Now()); err != nil {
		t.Fatal(err)
	}
	staged := errors.New("bounded deletion unnecessarily entered staged erasure")
	removed, err := s.deleteSourceStagedIf(ctx, o.ContentID(), func() error { return staged }, func(bolt.Reader) (bool, error) { return true, nil })
	if err != nil || !removed {
		t.Fatal("bounded conditional deletion did not commit atomically", removed, err)
	}
	if _, found, err := s.GetObservation(ctx, o.ContentID()); err != nil || found {
		t.Fatal("atomic deletion retained source", found, err)
	}
}

func TestConditionalDeletionFallsBackAfterAtomicBudgetRollback(t *testing.T) {
	s := openReplicaTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	o, _ := capture.SanitizeObservation("reader.app", "example.com", "Repeated source needs resumable erasure", 512)
	if err := s.PutObservationAt(ctx, o, at); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 12; i++ {
		if err := s.RecordObservationSighting(ctx, o, at.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	staged := false
	removed, err := s.deleteSourceStagedIf(ctx, o.ContentID(), func() error {
		staged = true
		if _, _, err := s.GetObservation(ctx, o.ContentID()); !errors.Is(err, ErrDeletionPending) {
			t.Fatal("partial rollback/erasure became visible", err)
		}
		return nil
	}, func(bolt.Reader) (bool, error) { return true, nil })
	if err != nil || !removed || !staged {
		t.Fatal("budget fallback failed", removed, staged, err)
	}
	if _, found, err := s.GetObservation(ctx, o.ContentID()); err != nil || found {
		t.Fatal("resumable fallback retained source", found, err)
	}
}

func TestPermanentCutoffsPreserveLegacyHistoryBeyondOldLogCapacity(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xd9)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	legacy := make([]Tombstone, MaxTombstones)
	for i := range legacy {
		legacy[i] = Tombstone{SourceID: fmt.Sprintf("%064x", i+1), At: at.Add(time.Duration(i) * time.Second)}
	}
	if err := s.Update(ctx, func(tx *Tx) error { return tx.PutIndexRecord(tombstoneNS, tombstoneLogID, encodeTombstones(legacy)) }); err != nil {
		t.Fatal(err)
	}
	// Simulate schema upgrade before the first fence migration checkpoint.
	if err := s.Update(ctx, func(tx *Tx) error {
		if err := tx.deleteKnowledgeRecord(captureTimeMigrationNamespace, deletionMigrationID); err != nil {
			return err
		}
		return tx.deleteKnowledgeRecord(deletionCutoffNS, deletionSequenceID)
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
	t.Cleanup(func() { _ = s.Close() })
	if err := s.RecordTombstone(ctx, Tombstone{SourceID: fmt.Sprintf("%064x", MaxTombstones+1), At: at.Add(24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	reader, ok := any(s).(cutoffReader)
	if !ok {
		t.Fatal("store has no permanent cutoff read capability")
	}
	for _, i := range []int{0, MaxTombstones / 2, MaxTombstones - 1} {
		covered, err := reader.ObservationDeletionCovers(ctx, legacy[i].SourceID, legacy[i].At)
		if err != nil || !covered {
			t.Fatalf("old deletion %d was lost after log capacity: %t %v", i, covered, err)
		}
	}
}
