package replica

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage/durable"
)

type deletionAfterImportPreflight struct {
	*durable.Store
	id      string
	at      time.Time
	deleted bool
}

func (s *deletionAfterImportPreflight) GetObservation(ctx context.Context, id string) (capture.SanitizedObservation, bool, error) {
	o, found, err := s.Store.GetObservation(ctx, id)
	if err == nil && id == s.id && !s.deleted {
		s.deleted = true
		if err := s.RecordTombstone(ctx, durable.Tombstone{SourceID: id, At: s.at}); err != nil {
			return o, found, err
		}
		if err := s.DeleteObservationRecord(ctx, id); err != nil {
			return o, found, err
		}
	}
	return o, found, err
}

func TestImporterSkipsDeletionCommittedAfterItsPreflight(t *testing.T) {
	for _, note := range []bool{false, true} {
		t.Run(fmt.Sprint("note=", note), func(t *testing.T) {
			ctx := context.Background()
			folder, key := t.TempDir(), testKey(t, "fenced import")
			a, b := newDevice(t, folder, key), newDevice(t, folder, key)
			base := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
			app, domain := "reader.app", "example.com"
			if note {
				app, domain = "sh.blur.notes", "local"
			}
			o, _ := capture.SanitizeObservation(app, domain, "Stale source from another device", 512)
			if err := a.store.PutObservationAt(ctx, o, base); err != nil {
				t.Fatal(err)
			}
			if _, err := a.w.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			b.im.Store = &deletionAfterImportPreflight{Store: b.store, id: o.ContentID(), at: base.Add(time.Hour)}
			b.im.Now = func() time.Time { return base.Add(2 * time.Hour) }
			result, err := b.im.Run(ctx)
			if err != nil || result.Segments != 1 || result.Skipped != 1 || result.Observations != 0 || result.Rejected != 0 {
				t.Fatalf("stale admission was not a clean skip: %+v %v", result, err)
			}
			if has(t, b, o.ContentID()) {
				t.Fatal("import resurrected the deleted source")
			}
			if note {
				snapshot, found, err := b.store.GetTodaySnapshot(ctx, "2026-11-13")
				if err != nil || found && len(snapshot.SourceIDs) != 0 {
					t.Fatalf("skipped imported note created Today content: %+v %t %v", snapshot, found, err)
				}
			}
		})
	}
}

func TestLateRelayedDeletionExportsDespiteEarlierOriginalTimestamp(t *testing.T) {
	ctx := context.Background()
	folder, key := t.TempDir(), testKey(t, "late deletion")
	a, b := newDevice(t, folder, key), newDevice(t, folder, key)
	base := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	for i, at := range []time.Time{base.Add(2 * time.Hour), base.Add(time.Hour)} {
		if err := a.store.RecordTombstone(ctx, durable.Tombstone{SourceID: fmt.Sprintf("%064x", i+1), At: at}); err != nil {
			t.Fatal(err)
		}
		if _, err := a.w.Flush(ctx); err != nil {
			t.Fatal(err)
		}
	}
	result, err := b.im.Run(ctx)
	if err != nil || result.Segments != 2 || result.Tombstones != 2 {
		t.Fatalf("time watermark lost a late relayed deletion: %+v %v", result, err)
	}
	covered, err := b.store.ObservationDeletionCovers(ctx, fmt.Sprintf("%064x", 2), base)
	if err != nil || !covered {
		t.Fatalf("late cutoff never reached other device: %t %v", covered, err)
	}
}

func TestOlderRelayedDeletionPreservesGenuinelyNewerCapture(t *testing.T) {
	ctx := context.Background()
	folder, key := t.TempDir(), testKey(t, "newer capture")
	a, b := newDevice(t, folder, key), newDevice(t, folder, key)
	base := time.Now().UTC().Add(-3 * time.Hour)
	id := observe(t, b, "A genuine later visit", base.Add(2*time.Hour))
	if err := a.store.RecordTombstone(ctx, durable.Tombstone{SourceID: id, At: base.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if result, err := b.im.Run(ctx); err != nil || result.Tombstones != 1 {
		t.Fatal(result, err)
	}
	if !has(t, b, id) {
		t.Fatal("an older relayed deletion erased a genuinely newer capture")
	}
}

func TestSkippedImportDoesNotSuppressLaterLocalRecaptureExport(t *testing.T) {
	ctx := context.Background()
	folder, key := t.TempDir(), testKey(t, "recapture origin")
	a, b, c := newDevice(t, folder, key), newDevice(t, folder, key), newDevice(t, folder, key)
	base := time.Now().UTC().Add(-3 * time.Hour)
	id := observe(t, a, "Memory later captured again", base)
	if _, err := a.w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	b.im.Store = &deletionAfterImportPreflight{Store: b.store, id: id, at: base.Add(time.Hour)}
	if result, err := b.im.Run(ctx); err != nil || result.Skipped != 1 {
		t.Fatal(result, err)
	}
	observe(t, b, "Memory later captured again", base.Add(2*time.Hour))
	if _, err := b.w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.im.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if !has(t, c, id) {
		t.Fatal("skipped imported origin suppressed later local capture")
	}
}

func TestImporterRecoversCommittedErasureBeforeFirstStoreOperation(t *testing.T) {
	for _, tombstone := range []bool{false, true} {
		t.Run(fmt.Sprint("tombstone=", tombstone), func(t *testing.T) {
			ctx := context.Background()
			folder, key := t.TempDir(), testKey(t, "erasure recovery")
			a, b := newDevice(t, folder, key), newDevice(t, folder, key)
			base := time.Now().UTC().Add(-time.Hour)
			old := observe(t, b, "A source whose erasure already committed", base)
			if err := b.store.RecordTombstone(ctx, durable.Tombstone{SourceID: old, At: base.Add(time.Minute)}); err != nil {
				t.Fatal(err)
			}
			// Authenticated phase-7 intent represents an earlier interrupted
			// cascade. Trying a different source activates the actual store gate.
			if err := b.store.Update(ctx, func(tx *durable.Tx) error {
				return tx.PutIndexRecord("source-erase-v1", "pending", []byte(fmt.Sprintf(`{"source_id":%q,"phase":7}`, old)))
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := b.store.DeleteObservationRecordBefore(ctx, fmt.Sprintf("%064x", 1), base, true, nil); !errors.Is(err, durable.ErrDeletionPending) {
				t.Fatal("fixture did not activate erasure gate", err)
			}
			fresh := ""
			if tombstone {
				if err := a.store.RecordTombstone(ctx, durable.Tombstone{SourceID: old, At: base.Add(time.Minute)}); err != nil {
					t.Fatal(err)
				}
			} else {
				fresh = observe(t, a, "A new source after an interrupted deletion", base.Add(2*time.Minute))
			}
			if _, err := a.w.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			result, err := b.im.Run(ctx)
			if err != nil || result.Segments != 1 || result.Rejected != 0 {
				t.Fatal("import never recovered its gated store", result, err)
			}
			if has(t, b, old) {
				t.Fatal("committed erasure was not finished")
			}
			if fresh != "" && !has(t, b, fresh) {
				t.Fatal("source-only segment failed after recovery")
			}
		})
	}
}

func TestRunnerDrainsPagedDeletionChangesWithoutWaitingForNextInterval(t *testing.T) {
	for _, mode := range []string{"Sync", "FlushNow"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			folder, key := t.TempDir(), testKey(t, "paged runner")
			a, b := newDevice(t, folder, key), newDevice(t, folder, key)
			at := time.Now().UTC().Add(-time.Hour)
			for i := 0; i < durable.MaxDeletionChangePage+1; i++ {
				if err := a.store.RecordTombstone(ctx, durable.Tombstone{SourceID: fmt.Sprintf("%064x", i+1), At: at}); err != nil {
					t.Fatal(err)
				}
			}
			runner := &Runner{Writer: a.w, Importer: a.im}
			run := func(ctx context.Context) error {
				if mode == "FlushNow" {
					return runner.FlushNow(ctx)
				}
				_, err := runner.Sync(ctx)
				return err
			}
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			if err := run(cancelled); !errors.Is(err, context.Canceled) {
				t.Fatal("cancelled drain ignored caller deadline", err)
			}
			if st := a.state.Get(); st.TombSequence != 0 || st.LastSeq != 0 {
				t.Fatal("cancelled drain advanced export state", st.TombSequence, st.LastSeq)
			}
			if err := run(ctx); err != nil {
				t.Fatal(err)
			}
			result, err := b.im.Run(ctx)
			if err != nil || result.Tombstones != durable.MaxDeletionChangePage+1 {
				t.Fatal("deletion page was left until next background interval", result, err)
			}
			reopened, err := OpenState(a.state.path)
			if err != nil || reopened.Get().TombSequence != durable.MaxDeletionChangePage+1 {
				t.Fatal("sequence cursor did not persist", err)
			}
		})
	}
}
