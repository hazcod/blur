package durable

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/semantic"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/tasks"
)

func TestNoteDocumentRevisionKeepsProofAndRejectsLostUpdate(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xc2)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	first, err := s.CreateNoteDocument(ctx, "# Plan\n\noldwordalpha", at)
	if err != nil {
		t.Fatal(err)
	}
	old, _, err := s.GetObservation(ctx, first.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	vector := semantic.Document{SourceID: first.SourceID, AppID: old.AppID(), Domain: old.Domain(), CapturedAt: at, TextBytes: len(old.Text()), Model: semantic.Model{Name: "test-embedding", Digest: fmt.Sprintf("%064x", 8), Dimensions: 2, Format: semantic.FormatPlain}, Chunks: []semantic.Chunk{{Start: 0, End: len(old.Text()), Vector: []float32{0.8, 0.6}}}}
	if err := s.PutSemanticDocument(ctx, vector); err != nil {
		t.Fatal(err)
	}
	second, err := s.UpdateNoteDocument(ctx, first.ID, 1, "# Plan\n\nnewwordbeta", at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Revision != 2 || second.SourceID == first.SourceID || second.Text != "# Plan\n\nnewwordbeta" {
		t.Fatalf("revision: %+v", second)
	}
	if _, err = s.UpdateNoteDocument(ctx, first.ID, 1, "lost update", at.Add(2*time.Minute)); !errors.Is(err, notes.ErrDocumentConflict) {
		t.Fatalf("conflict: %v", err)
	}
	if _, found, err := s.GetSemanticDocument(ctx, first.SourceID); err != nil || found {
		t.Fatalf("old semantic document stayed current: %v %v", found, err)
	}
	if err := s.PutSemanticDocument(ctx, vector); !errors.Is(err, semantic.ErrSourceGone) {
		t.Fatalf("late vector generation accepted: %v", err)
	}
	window, err := s.ListObservationWindow(ctx, storage.ObservationWindowRequest{Start: at.Add(-time.Hour), End: at.Add(time.Hour), AppID: "sh.blur.notes", Limit: 10, MaxBytes: 2 << 20})
	if err != nil || len(window.Observations) != 1 || window.Observations[0].ContentID() != second.SourceID {
		t.Fatalf("timeline includes old revision: %+v %v", window, err)
	}
	proof, found, err := s.GetObservation(ctx, first.SourceID)
	if err != nil || !found || proof.Text() != "# Plan\n\noldwordalpha" {
		t.Fatalf("immutable proof: %v %v", found, err)
	}
	for _, tc := range []struct {
		term  string
		count int
	}{{"oldwordalpha", 0}, {"newwordbeta", 1}} {
		page, err := s.SearchExact(ctx, search.ExactQuery{Terms: []string{tc.term}, TopK: 10, MaxRows: 256, MaxBytes: 2 << 20})
		if err != nil || len(page.Hits) != tc.count {
			t.Fatalf("search %s: %+v %v", tc.term, page, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetNoteDocument(ctx, first.SourceID)
	if err != nil || !found || got.SourceID != second.SourceID || got.Revision != 2 {
		t.Fatalf("reopen: %+v %v %v", got, found, err)
	}
}

func TestNoteDocumentRevisionRetiresSuggestionsAndInferredFactsWithoutFragmentingSubjects(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xc6), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	first, err := s.CreateNoteDocument(ctx, "Atlas is due Friday.", at)
	if err != nil {
		t.Fatal(err)
	}
	proposal := notes.Proposal{ID: "old-task", Kind: notes.Task, Text: "Finish Atlas on Friday", SourceID: first.SourceID, CreatedAt: at}
	if err := s.SaveProposals(ctx, first.SourceID, []notes.Proposal{proposal}); err != nil {
		t.Fatal(err)
	}
	chunk := enrich.Chunk{SourceID: first.SourceID, ModelKey: enrich.ModelKey("fixture"), End: len(first.Text), Next: len(first.Text), Complete: true, Proposals: []derive.Proposal{{Kind: knowledge.Project, Name: "Atlas", Fact: first.Text, SourceID: first.SourceID}}}
	if err := s.ApplyEnrichment(ctx, chunk); err != nil {
		t.Fatal(err)
	}
	id := enrich.EntityID(knowledge.Project, "Atlas", "sh.blur.notes", "local")
	if e, found, err := s.GetInferredKnowledge(ctx, id); err != nil || !found || len(e.Facts) != 1 {
		t.Fatalf("initial inference: %+v %v %v", e, found, err)
	}
	second, err := s.UpdateNoteDocument(ctx, first.ID, 1, "Atlas is due Monday.", at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if e, found, err := s.GetInferredKnowledge(ctx, id); err != nil || found {
		t.Fatalf("superseded inferred fact stayed current: %+v %v %v", e, found, err)
	}
	if err := s.ApplyEnrichment(ctx, chunk); !errors.Is(err, enrich.ErrSourceGone) {
		t.Fatalf("late old-revision inference was accepted: %v", err)
	}
	day, found, err := s.GetTodaySnapshot(ctx, "2026-11-14")
	if err != nil || !found || len(day.TodoCandidates) != 0 {
		t.Fatalf("superseded suggestion stayed current: %+v %v %v", day, found, err)
	}
	chunk.SourceID = second.SourceID
	chunk.End = len(second.Text)
	chunk.Next = len(second.Text)
	chunk.Proposals[0].SourceID = second.SourceID
	chunk.Proposals[0].Fact = second.Text
	if err := s.ApplyEnrichment(ctx, chunk); err != nil {
		t.Fatal(err)
	}
	if e, found, err := s.GetInferredKnowledge(ctx, id); err != nil || !found || len(e.Facts) != 1 || e.Facts[0].Text != "Atlas is due Monday." {
		t.Fatalf("revision fragmented or retained old facts: %+v %v %v", e, found, err)
	}
	if err := s.DeleteNoteDocument(ctx, first.ID, 2); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{first.SourceID, second.SourceID} {
		if _, found, err := s.GetEnrichmentProgress(ctx, source); err != nil || found {
			t.Fatalf("deleted derived index remained: %v %v", found, err)
		}
	}
}

func TestNoteDocumentEditPreservesConfirmedTaskAndWholeDeleteResumes(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xc3)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	first, err := s.CreateNoteDocument(ctx, "privateoldproof", at)
	if err != nil {
		t.Fatal(err)
	}
	oldProof, _, err := s.GetObservation(ctx, first.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	item := taskFixture(87)
	item.SourceID = first.SourceID
	item.UserAuthored = false
	if err := s.PutTask(ctx, item, 0); err != nil {
		t.Fatal(err)
	}
	second, err := s.UpdateNoteDocument(ctx, first.ID, 1, "privatenewproof", at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetTask(ctx, item.ID)
	if err != nil || !found || got.Revision != item.Revision || got.Reminder == nil || !got.Reminder.DueAt.Equal(item.Reminder.DueAt) || got.Delivery != item.Delivery || got.SourceID != first.SourceID {
		t.Fatalf("editing changed confirmed task/reminder: %+v %v %v", got, found, err)
	}
	if err := s.SaveProposals(ctx, first.SourceID, []notes.Proposal{{ID: "late", Kind: notes.Task, Text: "Stale task", SourceID: first.SourceID, CreatedAt: at}}); !errors.Is(err, notes.ErrSourceUnavailable) {
		t.Fatalf("late source proposals: %v", err)
	}
	stop := errors.New("interrupted fixture")
	prepared := map[string]bool{}
	err = s.deleteNoteDocument(ctx, first.ID, 2, func(id string, items []tasks.Item) error {
		prepared[id] = true
		if id == first.SourceID && (len(items) != 1 || items[0].ID != item.ID) {
			t.Fatal("native cleanup lost old reminder")
		}
		return nil
	}, func() error { return stop })
	if !errors.Is(err, stop) || !prepared[first.SourceID] || !prepared[second.SourceID] {
		t.Fatalf("erasure preparation: %v %v", prepared, err)
	}
	if _, _, err := s.GetObservation(ctx, second.SourceID); !errors.Is(err, ErrDeletionPending) {
		t.Fatalf("partial delete must gate reads: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first.SourceID, second.SourceID} {
		if _, found, err := s.GetObservation(ctx, id); err != nil || found {
			t.Fatalf("erased revision %s survived: %v %v", id, found, err)
		}
	}
	if err := s.PutObservationAt(ctx, oldProof, at); !errors.Is(err, ErrSourceDeleted) {
		t.Fatalf("deleted source replay restored revision: %v", err)
	}
	if _, found, err := s.GetTask(ctx, item.ID); err != nil || found {
		t.Fatalf("whole deletion left task: %v %v", found, err)
	}
	page, err := s.ListNoteDocumentsPage(ctx, notes.DocumentPageRequest{Limit: 10, MaxBytes: 2 << 20})
	if err != nil || len(page.Documents) != 0 {
		t.Fatalf("deleted head still browsable: %+v %v", page, err)
	}
	if _, err := s.UpdateNoteDocument(ctx, first.ID, 2, "late resurrection", at.Add(time.Hour)); !errors.Is(err, notes.ErrDocumentNotFound) {
		t.Fatalf("late edit: %v", err)
	}
	for _, term := range []string{"privateoldproof", "privatenewproof"} {
		p, err := s.SearchExact(ctx, search.ExactQuery{Terms: []string{term}, TopK: 10, MaxRows: 256, MaxBytes: 2 << 20})
		if err != nil || len(p.Hits) != 0 {
			t.Fatalf("erased index: %+v %v", p, err)
		}
	}
}

func TestNoteDocumentDirectoryIsEncryptedAndChronologicalWithLazyLegacyAdmission(t *testing.T) {
	ctx := context.Background()
	path := durableTestPath(t)
	s, err := OpenDurable(path, durableTestKey(t, 0xc4), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	legacy, _ := capture.SanitizeObservation("sh.blur.notes", "local", "legacyprivatebody", 4096)
	if err := s.PutObservationAt(ctx, legacy, at.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	first, err := s.CreateNoteDocument(ctx, "identicalprivatebody", at)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateNoteDocument(ctx, "identicalprivatebody", at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || first.SourceID == second.SourceID {
		t.Fatal("distinct notes aliased by content")
	}
	updated, err := s.UpdateNoteDocument(ctx, first.ID, 1, "updatedprivatebody", at.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ListNoteDocumentsPage(ctx, notes.DocumentPageRequest{Limit: 1, MaxBytes: 2 << 20})
	if err != nil || len(page.Documents) != 1 || page.Documents[0].SourceID != updated.SourceID || !page.HasMore {
		t.Fatalf("first chronological page: %+v %v", page, err)
	}
	page, err = s.ListNoteDocumentsPage(ctx, notes.DocumentPageRequest{BeforeUpdatedAt: page.NextUpdatedAt, BeforeID: page.NextID, Limit: 2, MaxBytes: 2 << 20})
	if err != nil || len(page.Documents) != 2 || page.Documents[0].ID != second.ID || page.Documents[1].SourceID != legacy.ContentID() || page.HasMore {
		t.Fatalf("remaining chronological page: %+v %v", page, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, plain := range []string{"identicalprivatebody", "updatedprivatebody", "legacyprivatebody", first.ID, first.SourceID} {
		if bytes.Contains(data, []byte(plain)) {
			t.Fatalf("plaintext leaked into store: %s", plain)
		}
	}
}

func TestNoteDocumentFailedSaveRollsBackSourceAndHead(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xc5), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	original, err := s.CreateNoteDocument(ctx, "original remains", at.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]noteDocumentOrder, maxNoteDocumentsPerDay)
	for i := range rows {
		rows[i] = noteDocumentOrder{ID: fmt.Sprintf("%064x", len(rows)-i), At: at}
	}
	err = s.Update(ctx, func(tx *Tx) error {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		if err := tx.putIndexValueLocked(noteDocumentMetaNS, "days", mustJSON([]string{"2026-11-13", "2026-11-14"})); err != nil {
			return err
		}
		return tx.putIndexValueLocked(noteDocumentDayNS, "2026-11-14", mustJSON(rows))
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateNoteDocument(ctx, "mustnotcommit", at); !errors.Is(err, notes.ErrDocumentCapacity) {
		t.Fatalf("capacity should reject: %v", err)
	}
	if _, err := s.UpdateNoteDocument(ctx, original.ID, 1, "must not replace original", at); !errors.Is(err, notes.ErrDocumentCapacity) {
		t.Fatalf("failed edit did not reject: %v", err)
	}
	got, found, err := s.GetNoteDocument(ctx, original.ID)
	if err != nil || !found || got.SourceID != original.SourceID || got.Revision != 1 || got.Text != "original remains" {
		t.Fatalf("failed edit changed head: %+v %v %v", got, found, err)
	}
	page, err := s.ListObservationPage(ctx, storage.ObservationPageRequest{Limit: 16, MaxBytes: 2 << 20})
	if err != nil || len(page.Observations) != 1 || page.Observations[0].ContentID() != original.SourceID {
		t.Fatalf("failed transaction left source: %+v %v", page, err)
	}
	if _, err := s.CreateNoteDocument(ctx, "store still usable", at.Add(24*time.Hour)); err != nil {
		t.Fatalf("expected failure poisoned store: %v", err)
	}
}
