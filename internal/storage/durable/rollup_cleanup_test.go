package durable

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"testing"
	"time"
)

func TestCleanupGateRejectsFreshRevisionAndSourceErasureFindsPendingEdges(t *testing.T) {
	ctx := context.Background()
	s, _, _ := rollupTestStore(t, 0xcc)
	at := time.Date(2018, 2, 6, 9, 0, 0, 0, time.UTC)
	doomed := putHistoricalHour(t, s, at)
	kept := putHistoricalHour(t, s, at.Add(4*time.Hour))
	w := historicalRollupWorker(s)
	defer w.Close()
	for n := 0; n < 3; n++ {
		if err := w.ProcessRollups(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteChronicle(ctx, "hour:20180206T09Z"); err != nil {
		t.Fatal(err)
	}
	cleared, found, err := s.GetChronicleRollup(ctx, "day:2018-02-06")
	if err != nil || !found || cleared.Text != "" || len(cleared.SourceIDs) != 0 {
		t.Fatalf("input erasure did not clear public fields: %+v %v %v", cleared, found, err)
	}
	candidate := rollupTestRecord(t, cleared.ID, "- Reintroduced deleted-hour content.", []string{"hour:20180206T09Z"}, []string{doomed})
	candidate.Revision, candidate.Epoch = cleared.Revision, cleared.Epoch
	// This is deliberately a CURRENT revision, so only the pending-cleanup
	// publication gate, rather than stale-revision CAS, can reject it.
	if err := s.PutChronicleRollup(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	if row, _, err := s.GetChronicleRollup(ctx, cleared.ID); err != nil || row.Text != "" || len(row.SourceIDs) != 0 {
		t.Fatalf("fresh revision bypassed cleanup gate: %+v %v", row, err)
	}
	if err := s.DeleteObservationRecord(ctx, doomed); err != nil {
		t.Fatalf("source erasure could not follow a pending-only reverse edge: %v", err)
	}
	if err := s.Update(ctx, func(tx *Tx) error {
		for _, id := range []string{cleared.ID, "week:2018-W06"} {
			c, found, err := s.rollupCleanupReader(tx.writer, id)
			if err != nil {
				return err
			}
			if !found || !slices.Equal(c.SourceIDs, []string{kept}) {
				t.Fatalf("pending obligations lost or kept erased source: %+v %v", c, found)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 3; n++ {
		if err := w.ProcessRollups(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutChronicleRollup(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{cleared.ID, "week:2018-W06"} {
		row, found, err := s.GetChronicleRollup(ctx, id)
		if err != nil || !found || row.Dirty || row.Text == "" || row.Text == candidate.Text || !slices.Equal(row.SourceIDs, []string{kept}) {
			t.Fatalf("cleanup did not regenerate from surviving hour: %+v %v %v", row, found, err)
		}
	}
}

func TestHardRecapDeletionKeepsCleanupCataloguedAcrossRestartAndSourceErasure(t *testing.T) {
	ctx := context.Background()
	s, path, _ := rollupTestStore(t, 0xcd)
	at := time.Date(2018, 2, 6, 9, 0, 0, 0, time.UTC)
	var sources []string
	for n := 0; n < 96; n++ {
		sources = append(sources, rollupTestSource(t, s, fmt.Sprintf("Historical hard-delete source%d", n), at.Add(time.Minute)))
	}
	sort.Strings(sources)
	r := rollupTestRecord(t, "day:2018-02-06", "- Legacy saved recap.", []string{"hour:20180206T09Z"}, sources)
	installLegacyRollupForTest(t, s, r)
	if err := s.beginChronicleErasure(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, func(tx *Tx) error { _, err := tx.resumeChronicleErasure(); return err }); err != nil {
		t.Fatal(err)
	}
	var pendingSources []string
	if err := s.Update(ctx, func(tx *Tx) error {
		c, found, err := s.rollupCleanupReader(tx.writer, r.ID)
		if err != nil {
			return err
		}
		if !found || !c.Deleting || len(c.SourceIDs) != 64 {
			t.Fatalf("hard delete did not retain bounded obligations: %+v %v", c, found)
		}
		pendingSources = slices.Clone(c.SourceIDs)
		row, found, err := tx.readRollup(r.ID)
		if err != nil {
			return err
		}
		if !found || row.Text != "" || len(row.SourceIDs) != 0 || len(row.ChildIDs) != 0 {
			t.Fatalf("hard-delete row disappeared before cleanup or leaked fields: %+v %v", row, found)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	s, err = OpenDurable(path, durableTestKey(t, 0xcd), OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.DeleteObservationRecord(ctx, pendingSources[20]); err != nil {
		t.Fatalf("original erasure failed while derivative erasure was pending: %v", err)
	}
	if err := s.Update(ctx, func(tx *Tx) error {
		c, found, err := s.rollupCleanupReader(tx.writer, r.ID)
		if err != nil {
			return err
		}
		if !found || !c.Deleting || len(c.SourceIDs) != 63 || slices.Contains(c.SourceIDs, pendingSources[20]) {
			t.Fatalf("pending hard-delete receipt lost progress: %+v %v", c, found)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	resumeRecapErasureForTest(t, s)
	if err := s.Update(ctx, func(tx *Tx) error {
		if _, found, err := tx.readRollup(r.ID); err != nil {
			return err
		} else if found {
			t.Fatal("hard-deleted forward row remains")
		}
		if _, found, err := s.rollupCleanupReader(tx.writer, r.ID); err != nil {
			return err
		} else if found {
			t.Fatal("cleanup orphan remains after catalog became empty")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetSourceEvidence(ctx, sources[0]); err != nil || !found {
		t.Fatalf("recap clearing removed an unrelated original: %v %v", found, err)
	}
}
