package durable

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"github.com/hazcod/blur/internal/tasks"
)

func TestCapturePartDeletionPreparesWholeSourceAndCanRejectBeforeIntent(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	at := time.Now().UTC().Add(48 * time.Hour)
	summary := publishIndexedFixture(t, s, strings.Repeat("whole source exact data ", 3000)+"TAIL", at)
	drainCaptureIndex(t, s)
	ids, more, err := s.ListCaptureSourceIDs(ctx, summary.ContentID(), -1, 64)
	if err != nil || more || len(ids) < 2 {
		t.Fatal("fixture", len(ids), more, err)
	}
	for i, id := range ids {
		item := taskFixture(i + 1)
		item.UserAuthored = false
		item.SourceID = id
		item.SourceIDs = nil
		if err := s.PutTask(ctx, item, 0); err != nil {
			t.Fatal(err)
		}
	}
	refusal := errors.New("native cleanup unavailable")
	prepared := 0
	err = s.DeleteSourceRecordWithPrepare(ctx, ids[1], func(id string, items []tasks.Item) error {
		prepared++
		if id == ids[0] {
			if len(items) != 1 {
				t.Error("part task snapshot missing")
			}
			return refusal
		}
		return nil
	})
	if !errors.Is(err, refusal) || prepared != 2 {
		t.Fatal("preparation did not fail closed", prepared, err)
	}
	if _, found, err := s.GetCapture(ctx, summary.ContentID()); err != nil || !found {
		t.Fatal("native refusal deleted source", err)
	}
	seen := map[string]int{}
	err = s.DeleteSourceRecordWithPrepare(ctx, ids[1], func(id string, items []tasks.Item) error {
		seen[id]++
		if id != summary.ContentID() && len(items) != 1 {
			t.Error("missing sibling task")
		}
		return nil
	})
	if err != nil || len(seen) != 1+len(ids) {
		t.Fatal("whole-source preparation missing", seen, err)
	}
	if _, found, err := s.GetCapture(ctx, summary.ContentID()); err != nil || found {
		t.Fatal("part delete retained document", err)
	}
	for _, id := range ids {
		if _, found, err := s.GetSourceEvidence(ctx, id); err != nil || found {
			t.Fatal("part source survived", err)
		}
		items, err := s.TasksForSources(ctx, []string{id})
		if err != nil || len(items) != 0 {
			t.Fatal("part task survived", err)
		}
	}
}
func TestCaptureSourceReceiptAndSightingUseCanonicalPublicationAndActualVisits(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	first := time.Now().UTC().Add(-72 * time.Hour)
	var canonicalID, partID string
	for i, at := range []time.Time{first, first.Add(24 * time.Hour), first.Add(48 * time.Hour)} {
		h, p, summary := makeStoredCapture(t, "Mira owns the source rollback.", at)
		stageStoredCapture(t, s, h, p)
		receipt, err := s.PublishCaptureWithSourceReceipt(ctx, summary)
		if err != nil || !receipt.Committed || receipt.New != (i == 0) {
			t.Fatal("wrong publication receipt", receipt.New, err)
		}
		if i == 0 {
			canonicalID, partID = summary.ContentID(), receipt.SourceID
		} else if receipt.SourceID != partID || summary.ContentID() != canonicalID {
			t.Fatal("duplicate source identity changed")
		}
		again, err := s.PublishCaptureWithSourceReceipt(ctx, summary)
		if err != nil || !again.Committed || again.New || again.SourceID != partID {
			t.Fatal("idempotent publication was counted new", err)
		}
	}
	drainCaptureIndex(t, s)
	want := first.Add(24 * time.Hour)
	got, found, err := s.SourceEvidenceSightingAt(ctx, partID, want.Add(-time.Minute), want.Add(time.Minute))
	if err != nil || !found || !got.Equal(want) {
		t.Fatal("intermediate real revisit missing", got, found, err)
	}
	page, err := s.SearchExact(ctx, search.ExactQuery{Terms: []string{"mira"}, CapturedStart: want.Add(-time.Minute), CapturedEnd: want.Add(time.Minute), MaxRows: search.MaxIndexRows, MaxBytes: search.MaxIndexBytes})
	if err != nil || len(page.Hits) != 1 || page.Hits[0].SourceID != partID || !page.Hits[0].CapturedAt.Equal(want) {
		t.Fatal("public exact query missed real visit", page, err)
	}
	evidence, found, err := s.GetSourceEvidence(ctx, partID)
	if err != nil || !found || evidence.SourceID() == canonicalID {
		t.Fatal("source did not resolve as genuine part", err)
	}
	provenance, isPart := evidence.Provenance()
	if !isPart || provenance.EditionID != canonicalID || evidence.Validate() != nil {
		t.Fatal("part provenance lost")
	}
	if _, found, err := s.GetObservation(ctx, partID); err != nil || found {
		t.Fatal("part forged a legacy observation", err)
	}
}

func TestPublicExactCaptureMergeUsesOneRowsAndBytesBudget(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	observation, err := capture.SanitizeObservation("org.example.editor", "", "Mira legacy evidence", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservationAt(ctx, observation, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	publishIndexedFixture(t, s, "Mira full capture evidence", time.Now().UTC())
	drainCaptureIndex(t, s)
	q := search.ExactQuery{Terms: []string{"mira"}, TopK: 20, MaxRows: search.MaxIndexRows, MaxBytes: search.MaxIndexBytes}
	page, err := s.SearchExact(ctx, q)
	if err != nil || len(page.Hits) != 2 || page.Rows != 2 {
		t.Fatal("namespaces did not share real public query", page, err)
	}
	if page.Hits[0].SourceID >= page.Hits[1].SourceID {
		t.Fatal("global tie order not proven")
	}
	q.MaxRows = 1
	if _, err := s.SearchExact(ctx, q); !errors.Is(err, search.ErrIndexBudget) {
		t.Fatal("each namespace received a fresh row budget", err)
	}
	q.MaxRows = search.MaxIndexRows
	q.MaxBytes = page.Bytes - 1
	if _, err := s.SearchExact(ctx, q); !errors.Is(err, search.ErrIndexBudget) {
		t.Fatal("each namespace received a fresh byte budget", err)
	}
}

func TestCaptureSourceReceiptCommitsBeforeDuplicatePrivateCleanup(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	at := time.Now().UTC()
	publishIndexedFixture(t, s, "A long duplicate capture to clean outside preference admission", at)
	h, p, summary := makeStoredCapture(t, "A long duplicate capture to clean outside preference admission", at.Add(time.Second))
	stageStoredCapture(t, s, h, p)
	receipt, err := s.PublishCaptureWithSourceReceipt(ctx, summary)
	if err != nil || !receipt.Committed || receipt.New {
		t.Fatal("duplicate receipt", err)
	}
	if err := s.view(ctx, func(reader bolt.Reader) error {
		stage, _, found, err := s.captureStageReader(reader, h.CaptureID())
		if err != nil {
			return err
		}
		if !found || stage.PublishedID != summary.ContentID() || stage.Cleaned != 0 {
			return ErrCorrupt
		}
		return nil
	}); err != nil {
		t.Fatal("publication performed unbounded cleanup inside admission", err)
	}
	if err := s.DiscardCaptureStage(ctx, h.CaptureID()); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetCapture(ctx, summary.ContentID()); err != nil || !found {
		t.Fatal("cleanup deleted canonical source", err)
	}
}
