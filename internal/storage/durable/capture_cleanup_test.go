package durable

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"github.com/hazcod/blur/internal/tasks"
)

func captureRecordExists(t *testing.T, s *Store, id string) bool {
	t.Helper()
	found := false
	if err := s.view(context.Background(), func(r bolt.Reader) error {
		key := s.logicalKey(groupRecord, kindIndex, "index:"+captureNS, id)
		defer clear(key)
		var err error
		found, err = r.Has(key)
		return mapStorePhysicalError(err)
	}); err != nil {
		t.Fatal(err)
	}
	return found
}

func assertCapturePayloadAbsent(t *testing.T, s *Store, h capture.CaptureHeader, parts int) {
	t.Helper()
	for i := 0; i < parts; i++ {
		if captureRecordExists(t, s, capturePartKey(h.CaptureID(), int64(i))) {
			t.Fatalf("abandoned capture part %d survived", i)
		}
	}
	if captureRecordExists(t, s, "stage:"+h.CaptureID()) {
		t.Fatal("abandoned stage survived")
	}
}

func TestCaptureReopenDiscardsIncompleteAndDuplicateStages(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xd1)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	h, parts, summary := makeStoredCapture(t, strings.Repeat("A complete visible paragraph. ", 40000), at)
	stageStoredCapture(t, s, h, parts)
	if _, err := s.PublishCapture(ctx, summary); err != nil {
		t.Fatal(err)
	}
	dh, dp, ds := makeStoredCapture(t, strings.Repeat("A complete visible paragraph. ", 40000), at.Add(time.Second))
	stageStoredCapture(t, s, dh, dp)
	// Model a crash after commit and before PublishCapture's normal drain.
	if _, err := s.publishCaptureWithHook(ctx, ds, nil); err != nil {
		t.Fatal(err)
	}
	uh, up, _ := makeStoredCapture(t, strings.Repeat("Unfinished private paragraph. ", 40000), at)
	stageStoredCapture(t, s, uh, up[:len(up)/2])
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	assertCapturePayloadAbsent(t, s, dh, len(dp))
	assertCapturePayloadAbsent(t, s, uh, len(up)/2)
	if page, err := s.ReadCaptureParts(ctx, summary.ContentID(), -1, 8); err != nil || len(page.Parts) == 0 {
		t.Fatal("canonical published payload was discarded", err)
	}
	if visits, err := s.ListCaptureVisits(ctx, summary.ContentID(), "", 64); err != nil || len(visits.Visits) != 2 {
		t.Fatal("duplicate cleanup removed real capture visits", err)
	}
}

func TestCaptureDiscardClosesBeforePartDeletionAndResumes(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	h, parts, summary := makeStoredCapture(t, strings.Repeat("Pending text. ", 8000), time.Now().UTC())
	stageStoredCapture(t, s, h, parts)
	steps := 0
	err := s.discardCaptureStageWithHook(ctx, h.CaptureID(), func() error { steps++; return context.Canceled })
	if !errors.Is(err, context.Canceled) || steps != 1 {
		t.Fatal("no bounded committed interruption", steps, err)
	}
	if err := s.AppendCaptureParts(ctx, h, parts); !errors.Is(err, ErrCaptureClosed) {
		t.Fatal("append entered abandoned stage", err)
	}
	if _, err := s.PublishCapture(ctx, summary); !errors.Is(err, ErrCaptureClosed) {
		t.Fatal("abandoned stage published", err)
	}
	if err := s.BeginCapture(ctx, h); !errors.Is(err, ErrCaptureClosed) {
		t.Fatal("begin reopened cleaning stage", err)
	}
	if err := s.DiscardCaptureStage(ctx, h.CaptureID()); err != nil {
		t.Fatal(err)
	}
	assertCapturePayloadAbsent(t, s, h, len(parts))
	if err := s.DiscardCaptureStage(ctx, h.CaptureID()); err != nil {
		t.Fatal("discard retry was not idempotent", err)
	}
}

func TestCaptureGenericSourceErasureRemovesPayloadVisitsAndCatalogs(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	at := time.Now().UTC()
	h, parts, summary := makeStoredCapture(t, strings.Repeat("Generic source deletion text. ", 3000), at)
	stageStoredCapture(t, s, h, parts)
	if _, err := s.PublishCapture(ctx, summary); err != nil {
		t.Fatal(err)
	}
	dh, dp, ds := makeStoredCapture(t, strings.Repeat("Generic source deletion text. ", 3000), at.Add(time.Second))
	stageStoredCapture(t, s, dh, dp)
	if _, err := s.PublishCapture(ctx, ds); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteObservationRecord(ctx, summary.ContentID()); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetCapture(ctx, summary.ContentID()); err != nil || found {
		t.Fatal("generic deletion retained capture", found, err)
	}
	assertCapturePayloadAbsent(t, s, h, len(parts))
	assertCapturePayloadAbsent(t, s, dh, len(dp))
	for _, id := range []string{"receipt:" + h.CaptureID(), "receipt:" + dh.CaptureID(), "edition:" + summary.ContentID(), captureVisitKey(summary.ContentID(), 0)} {
		if captureRecordExists(t, s, id) {
			t.Fatal("capture-owned row survived generic erasure", id)
		}
	}
	if page, err := s.ListCaptures(ctx, "", 32); err != nil || len(page.Editions) != 0 {
		t.Fatal("catalog retained erased source", err)
	}
}

func TestCaptureConditionalErasurePreservesNewerRealVisit(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	at := time.Now().UTC().Add(-time.Hour)
	var id string
	for _, visit := range []time.Time{at, at.Add(2 * time.Minute)} {
		h, p, summary := makeStoredCapture(t, "Latest real visit matters", visit)
		stageStoredCapture(t, s, h, p)
		if _, err := s.PublishCapture(ctx, summary); err != nil {
			t.Fatal(err)
		}
		id = summary.ContentID()
	}
	prepared := false
	erased, err := s.DeleteObservationRecordBefore(ctx, id, at.Add(time.Minute), true, func(_ []tasks.Item) error { prepared = true; return nil })
	if err != nil || erased || prepared {
		t.Fatal("newer visit was selected for deletion", erased, prepared, err)
	}
	if _, found, err := s.GetCapture(ctx, id); err != nil || !found {
		t.Fatal("newer capture disappeared", found, err)
	}
}

func TestCaptureFamilyForgetFencesOtherOldEditionsAndPreservesNewerVisits(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	at := time.Now().UTC().Add(-time.Hour)
	oldH, oldP, oldS := makeStoredCapture(t, "Old edition of the page", at)
	stageStoredCapture(t, s, oldH, oldP)
	if _, err := s.PublishCapture(ctx, oldS); err != nil {
		t.Fatal(err)
	}
	newH, newP, newS := makeStoredCapture(t, "New edition with a later visit", at)
	stageStoredCapture(t, s, newH, newP)
	if _, err := s.PublishCapture(ctx, newS); err != nil {
		t.Fatal(err)
	}
	visitH, visitP, visitS := makeStoredCapture(t, "New edition with a later visit", at.Add(2*time.Minute))
	stageStoredCapture(t, s, visitH, visitP)
	if _, err := s.PublishCapture(ctx, visitS); err != nil {
		t.Fatal(err)
	}
	pendingH, pendingP, pendingS := makeStoredCapture(t, "A different unpublished older edition", at.Add(time.Second))
	stageStoredCapture(t, s, pendingH, pendingP)
	oldIDs, _, err := s.ListCaptureSourceIDs(ctx, oldS.ContentID(), -1, 64)
	if err != nil {
		t.Fatal(err)
	}
	var prepared []string
	n, err := s.ForgetCaptureFamily(ctx, oldH.FamilyID(), at.Add(time.Minute), func(id string, _ []tasks.Item) error { prepared = append(prepared, id); return nil })
	if err != nil || n != 1 || len(prepared) != 1+len(oldIDs) || prepared[0] != oldS.ContentID() || !slices.Equal(prepared[1:], oldIDs) {
		t.Fatal("incorrect family selection", n, prepared, err)
	}
	if _, found, err := s.GetCapture(ctx, oldS.ContentID()); err != nil || found {
		t.Fatal("old family edition retained", found, err)
	}
	if got, found, err := s.GetCapture(ctx, newS.ContentID()); err != nil || !found || got.Visits != 2 {
		t.Fatal("newer visit erased", found, err)
	}
	assertCapturePayloadAbsent(t, s, pendingH, len(pendingP))
	if err := s.BeginCapture(ctx, pendingH); !errors.Is(err, ErrSourceDeleted) {
		t.Fatal("stale begin bypassed family fence", err)
	}
	if err := s.AppendCaptureParts(ctx, pendingH, pendingP); !errors.Is(err, ErrSourceDeleted) {
		t.Fatal("stale append bypassed family fence", err)
	}
	if _, err := s.PublishCapture(ctx, pendingS); !errors.Is(err, ErrSourceDeleted) {
		t.Fatal("stale publication bypassed family fence", err)
	}
	otherH, _, _ := makeStoredCapture(t, "Never previously staged older content", at)
	if err := s.BeginCapture(ctx, otherH); !errors.Is(err, ErrSourceDeleted) {
		t.Fatal("different edition resurrected forgotten family", err)
	}
	freshH, freshP, freshS := makeStoredCapture(t, "Fresh capture after forget", at.Add(3*time.Minute))
	stageStoredCapture(t, s, freshH, freshP)
	if _, err := s.PublishCapture(ctx, freshS); err != nil {
		t.Fatal("new real capture rejected", err)
	}
}

func TestCaptureFamilyIntentRequiresAllNativePreparation(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	at := time.Now().UTC().Add(-time.Hour)
	h, p, summary := makeStoredCapture(t, "Evidence linked to a native reminder", at)
	stageStoredCapture(t, s, h, p)
	if _, err := s.PublishCapture(ctx, summary); err != nil {
		t.Fatal(err)
	}
	item := taskFixture(1)
	item.UserAuthored, item.SourceID = false, summary.ContentID()
	// Seed an authenticated derivative directly: capture producers are wired
	// later, but their erasure must already honor native preparation snapshots.
	if err := s.taskUpdate(ctx, func(tx *Tx) error {
		if err := tx.putIndexValueLocked(taskSourceNS, summary.ContentID(), mustJSON([]string{item.ID})); err != nil {
			return err
		}
		return tx.putTaskLocked(item)
	}); err != nil {
		t.Fatal(err)
	}
	prepared := false
	_, err := s.ForgetCaptureFamily(ctx, h.FamilyID(), at.Add(time.Minute), func(id string, items []tasks.Item) error {
		if id != summary.ContentID() || len(items) != 1 || items[0].ID != item.ID {
			t.Fatal("wrong native preparation snapshot")
		}
		prepared = true
		return context.Canceled
	})
	if !prepared || !errors.Is(err, context.Canceled) {
		t.Fatal("preparation did not stop intent", prepared, err)
	}
	if _, found, err := s.GetCapture(ctx, summary.ContentID()); err != nil || !found {
		t.Fatal("unconfirmed intent erased data or retained gate", found, err)
	}
	if covered, err := s.ObservationDeletionCovers(ctx, h.FamilyID(), at); err != nil || covered {
		t.Fatal("unconfirmed intent committed fence", covered, err)
	}
}

func TestCaptureRetentionUsesLatestVisitAndDrainsPendingJobs(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	at := time.Now().UTC().Add(-time.Hour)
	h, p, summary := makeStoredCapture(t, "Expired document", at)
	stageStoredCapture(t, s, h, p)
	if _, err := s.PublishCapture(ctx, summary); err != nil {
		t.Fatal(err)
	}
	freshH, freshP, freshS := makeStoredCapture(t, "Boundary document", at.Add(time.Minute))
	stageStoredCapture(t, s, freshH, freshP)
	if _, err := s.PublishCapture(ctx, freshS); err != nil {
		t.Fatal(err)
	}
	pendingH, pendingP, _ := makeStoredCapture(t, "Expired pending job", at)
	stageStoredCapture(t, s, pendingH, pendingP)
	cutoff := at.Add(time.Minute)
	n, err := s.DeleteCapturesBefore(ctx, &cutoff, nil)
	if err != nil || n != 1 {
		t.Fatal("retention selected wrong captures", n, err)
	}
	assertCapturePayloadAbsent(t, s, h, len(p))
	assertCapturePayloadAbsent(t, s, pendingH, len(pendingP))
	if _, found, err := s.GetCapture(ctx, freshS.ContentID()); err != nil || !found {
		t.Fatal("strict cutoff deleted boundary capture", found, err)
	}
	if _, err := s.PublishCapture(ctx, summary); !errors.Is(err, ErrSourceDeleted) {
		t.Fatal("expired edition publication resurrected source", err)
	}
}

func TestCaptureFamilyErasureRecoversNewPhasesBeforeServing(t *testing.T) {
	for stop := 8; stop <= 13; stop++ {
		t.Run(fmt.Sprint(stop), func(t *testing.T) {
			ctx := context.Background()
			path, key := durableTestPath(t), durableTestKey(t, byte(0xb0+stop))
			s, err := OpenDurable(path, key, CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			at := time.Now().UTC().Add(-time.Hour)
			h, parts, summary := makeStoredCapture(t, strings.Repeat("Recover ordered document. ", 3000), at)
			stageStoredCapture(t, s, h, parts)
			if _, err := s.PublishCapture(ctx, summary); err != nil {
				t.Fatal(err)
			}
			dh, dp, ds := makeStoredCapture(t, strings.Repeat("Recover ordered document. ", 3000), at.Add(time.Second))
			stageStoredCapture(t, s, dh, dp)
			// Keep the crash window between atomic publication and its normal drain.
			if _, err := s.publishCaptureWithHook(ctx, ds, nil); err != nil {
				t.Fatal(err)
			}
			ph, pp, _ := makeStoredCapture(t, "Unfinished older content", at)
			stageStoredCapture(t, s, ph, pp)
			fh, fp, fs := makeStoredCapture(t, "New real capture survives", at.Add(2*time.Minute))
			stageStoredCapture(t, s, fh, fp)
			if _, err := s.PublishCapture(ctx, fs); err != nil {
				t.Fatal(err)
			}
			cutoff := at.Add(time.Minute)
			prepared := 0
			_, err = s.deleteCapturesWithHook(ctx, h.FamilyID(), &cutoff, true, func(string, []tasks.Item) error { prepared++; return nil }, func() error {
				j, found, err := s.readSourceErasureLocked(ctx)
				if err != nil || !found {
					t.Fatal("missing committed journal", found, err)
				}
				if stop == 10 && j.Phase == 10 {
					progressed := false
					if err := s.captureErasureViewLocked(ctx, func(r bolt.Reader) error {
						stage, _, found, err := s.captureStageReader(r, dh.CaptureID())
						progressed = found && stage.Cleaned > 0
						return err
					}); err != nil {
						t.Fatal(err)
					}
					if !progressed {
						return nil
					}
				}
				if j.Phase == stop && (stop != 8 || j.Part > 0) && (stop != 9 || j.VisitPage > 0) && (stop != 12 || j.Removed > 0) {
					return context.Canceled
				}
				return nil
			})
			if !errors.Is(err, context.Canceled) || prepared != 1+len(parts) {
				t.Fatal("did not interrupt expected committed phase", stop, prepared, err)
			}
			if _, _, err := s.GetCapture(ctx, fs.ContentID()); !errors.Is(err, ErrDeletionPending) {
				t.Fatal("partial erasure was readable", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenDurable(path, key, OpenExisting)
			if err != nil {
				t.Fatal("restart did not recover capture erasure", err)
			}
			assertCapturePayloadAbsent(t, s, h, len(parts))
			assertCapturePayloadAbsent(t, s, dh, len(dp))
			assertCapturePayloadAbsent(t, s, ph, len(pp))
			if _, found, err := s.GetCapture(ctx, summary.ContentID()); err != nil || found {
				t.Fatal("erased edition reappeared", found, err)
			}
			if _, found, err := s.GetCapture(ctx, fs.ContentID()); err != nil || !found {
				t.Fatal("newer capture lost on recovery", found, err)
			}
			if err := s.BeginCapture(ctx, ph); !errors.Is(err, ErrSourceDeleted) {
				t.Fatal("family fence lost on recovery", err)
			}
		})
	}
}

func TestCaptureDiscardRecoveryNeverDeletesCanonicalPayload(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	h, p, summary := makeStoredCapture(t, "Canonical owner is never a pending stage", time.Now().UTC())
	stageStoredCapture(t, s, h, p)
	if _, err := s.PublishCapture(ctx, summary); err != nil {
		t.Fatal(err)
	}
	if err := s.DiscardCaptureStage(ctx, h.CaptureID()); err != nil {
		t.Fatal(err)
	}
	if page, err := s.ReadCaptureParts(ctx, summary.ContentID(), -1, 8); err != nil || len(page.Parts) != 1 {
		t.Fatal("canonical payload discarded", err)
	}
}

func TestCaptureDiscardRejectsCanonicalOwnerInPendingCatalog(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	h, p, summary := makeStoredCapture(t, "Canonical owner cannot be mistaken for staging", time.Now().UTC())
	stageStoredCapture(t, s, h, p)
	if _, err := s.PublishCapture(ctx, summary); err != nil {
		t.Fatal(err)
	}
	header, err := capture.MarshalAuthenticatedCaptureHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.captureUpdate(ctx, func(tx *Tx) error {
		return tx.captureLinkStageLocked(h.CaptureID(), captureStageRecord{Version: 1, Header: header, Root: summary.Root(), Parts: summary.PartCount(), Bytes: summary.Bytes(), MaxInput: summary.RawBytes()})
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DiscardCaptureStage(ctx, h.CaptureID()); !errors.Is(err, ErrCorrupt) {
		t.Fatal("canonical owner was deleted by staging cleanup", err)
	}
	if page, err := s.ReadCaptureParts(ctx, summary.ContentID(), -1, 8); err != nil || len(page.Parts) != 1 {
		t.Fatal("rejected cleanup damaged canonical payload", err)
	}
}

func TestCaptureSourceErasureDrainsMoreThanOneVisitPage(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	at := time.Now().UTC().Add(-time.Hour)
	var headers []capture.CaptureHeader
	var parts [][]capture.CapturePart
	var summary capture.CaptureSummary
	for i := 0; i < 70; i++ {
		h, p, sum := makeStoredCapture(t, "Seventy exact document visits", at.Add(time.Duration(i)*time.Second))
		stageStoredCapture(t, s, h, p)
		if _, err := s.PublishCapture(ctx, sum); err != nil {
			t.Fatal(err)
		}
		headers, parts, summary = append(headers, h), append(parts, p), sum
	}
	if err := s.DeleteObservationRecord(ctx, summary.ContentID()); err != nil {
		t.Fatal(err)
	}
	for i, h := range headers {
		assertCapturePayloadAbsent(t, s, h, len(parts[i]))
		if captureRecordExists(t, s, "receipt:"+h.CaptureID()) {
			t.Fatal("receipt survived paged erasure")
		}
	}
	for i := int64(0); i < 2; i++ {
		if captureRecordExists(t, s, captureVisitKey(summary.ContentID(), i)) {
			t.Fatal("visit page survived erasure")
		}
	}
}

func TestCaptureRetentionAtUnixEpochPreservesBoundaryAndLaterVisits(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	epoch := time.Unix(0, 0).UTC()
	var ids []string
	for i, at := range []time.Time{epoch.Add(-time.Second), epoch, epoch.Add(time.Second)} {
		h, p, summary := makeStoredCapture(t, fmt.Sprintf("Epoch boundary fixture %d", i), at)
		stageStoredCapture(t, s, h, p)
		if _, err := s.PublishCapture(ctx, summary); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, summary.ContentID())
	}
	if n, err := s.DeleteCapturesBefore(ctx, &epoch, nil); err != nil || n != 1 {
		t.Fatal("epoch cutoff was treated as missing", n, err)
	}
	for _, id := range ids[1:] {
		if _, found, err := s.GetCapture(ctx, id); err != nil || !found {
			t.Fatal("epoch boundary or later visit erased", found, err)
		}
	}
}

func TestCaptureStagingReopenResumesAfterCommittedPartDeletion(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xb7)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	h, p, _ := makeStoredCapture(t, strings.Repeat("Interrupted pending capture. ", 5000), time.Now().UTC())
	stageStoredCapture(t, s, h, p)
	steps := 0
	err = s.discardCaptureStageWithHook(ctx, h.CaptureID(), func() error {
		steps++
		if steps == 2 {
			return context.Canceled
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cleanup did not stop after one part", err)
	}
	if captureRecordExists(t, s, capturePartKey(h.CaptureID(), 0)) || !captureRecordExists(t, s, capturePartKey(h.CaptureID(), 1)) {
		t.Fatal("stage cleanup did not yield after one part")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal("part cleanup could not resume on open", err)
	}
	assertCapturePayloadAbsent(t, s, h, len(p))
}

func TestCaptureAllFenceClosesOlderJobsAndKeepsStrictBoundary(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	at := time.Now().UTC().Add(-time.Hour)
	h, p, summary := makeStoredCapture(t, "Old published document", at)
	stageStoredCapture(t, s, h, p)
	if _, err := s.PublishCapture(ctx, summary); err != nil {
		t.Fatal(err)
	}
	uh, up, us := makeStoredCapture(t, "Different incomplete old document", at)
	stageStoredCapture(t, s, uh, up)
	cutoff := at.Add(time.Minute)
	if err := s.RecordTombstone(ctx, Tombstone{SourceID: AllSourcesTombstone, At: cutoff}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.DeleteCapturesBefore(ctx, nil, nil); err != nil || n != 1 {
		t.Fatal("all-source cleanup failed", n, err)
	}
	assertCapturePayloadAbsent(t, s, h, len(p))
	assertCapturePayloadAbsent(t, s, uh, len(up))
	if err := s.BeginCapture(ctx, uh); !errors.Is(err, ErrSourceDeleted) {
		t.Fatal("all fence allowed older begin", err)
	}
	if err := s.AppendCaptureParts(ctx, uh, up); !errors.Is(err, ErrSourceDeleted) {
		t.Fatal("all fence allowed older append", err)
	}
	if _, err := s.PublishCapture(ctx, us); !errors.Is(err, ErrSourceDeleted) {
		t.Fatal("all fence allowed older finish", err)
	}
	bh, bp, bs := makeStoredCapture(t, "Exact all-cutoff boundary", cutoff)
	stageStoredCapture(t, s, bh, bp)
	if _, err := s.PublishCapture(ctx, bs); err != nil {
		t.Fatal("strict all cutoff rejected equal-time capture", err)
	}
}

func TestCaptureRetentionFencesLateJobsNotYetStaged(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xaf)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Now().UTC().Add(-time.Hour)
	h, p, summary := makeStoredCapture(t, "Old job delayed before its initial stage transaction", at)
	cutoff := at.Add(time.Minute)
	if _, err := s.DeleteCapturesBefore(ctx, &cutoff, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginCapture(ctx, h); !errors.Is(err, ErrSourceDeleted) {
		t.Fatal("late unstaged job bypassed retention cutoff", err)
	}
	if err := s.AppendCaptureParts(ctx, h, p); !errors.Is(err, ErrSourceDeleted) {
		t.Fatal("late parts bypassed retention cutoff", err)
	}
	if _, err := s.PublishCapture(ctx, summary); !errors.Is(err, ErrSourceDeleted) {
		t.Fatal("late finish bypassed retention cutoff", err)
	}
	// An older retention request must never weaken the cutoff already applied.
	older := cutoff.Add(-time.Minute)
	if _, err := s.DeleteCapturesBefore(ctx, &older, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginCapture(ctx, h); !errors.Is(err, ErrSourceDeleted) {
		t.Fatal("retention cutoff moved backwards", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BeginCapture(ctx, h); !errors.Is(err, ErrSourceDeleted) {
		t.Fatal("retention cutoff was lost on restart", err)
	}
	bh, bp, bs := makeStoredCapture(t, "Boundary is still a real admissible capture", cutoff)
	stageStoredCapture(t, s, bh, bp)
	if _, err := s.PublishCapture(ctx, bs); err != nil {
		t.Fatal("retention cutoff rejected equal-time capture", err)
	}
}

func TestCaptureThousandsOfPendingRecordsRecoverBoundedly(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xad)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Now().UTC()
	var staged []capture.CaptureHeader
	// 1,100 parts plus 1,100 stage rows exceed a physical transaction's
	// operation cap; recovery must use the authenticated bounded progress.
	for i := 0; i < 1100; i++ {
		h, p, _ := makeStoredCapture(t, "Pending captured document", at.Add(time.Duration(i)*time.Nanosecond))
		stageStoredCapture(t, s, h, p)
		staged = append(staged, h)
	}
	steps := 0
	err = s.discardCaptureStageWithHook(ctx, staged[0].CaptureID(), func() error {
		steps++
		if steps == 2 {
			return context.Canceled
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("stress cleanup did not interrupt after progress", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal("thousands of private records could not recover", err)
	}
	if err := s.view(ctx, func(r bolt.Reader) error {
		root, err := s.captureCatalogReader(r, "pending")
		if err != nil {
			return err
		}
		if root.Count != 0 {
			t.Fatal("pending stage records survived", root.Count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, h := range staged {
		assertCapturePayloadAbsent(t, s, h, 1)
	}
}
