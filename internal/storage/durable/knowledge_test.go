package durable

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge"
)

func TestKnowledgeIsEncryptedIndexedPagedAndReopenable(t *testing.T) {
	ctx := context.Background()
	path := durableTestPath(t)
	key := durableTestKey(t, 0x81)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"entity-b", "entity-a", "entity-c"} {
		e := knowledge.Entity{ID: id, Kind: knowledge.Project, Name: "Project " + id, Aliases: []string{"Alias " + id}, State: knowledge.Confirmed, Facts: []knowledge.Fact{{ID: "fact-" + id, Text: "explicit task", State: knowledge.Confirmed, Sources: []knowledge.Source{{ID: repeatHex("a")}}}}}
		if err := s.PutKnowledgeEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ListKnowledge(ctx, knowledge.PageRequest{Limit: 2})
	if err != nil || len(page.Entities) != 2 || !page.HasMore || page.Next != "entity-b" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	match, found, err := s.SearchKnowledgeExact(ctx, "  ALIAS   entity-a ")
	if err != nil || !found || match.ID != "entity-a" {
		t.Fatalf("exact match=%+v found=%v err=%v", match, found, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	physical, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range [][]byte{[]byte("Project entity-a"), []byte("Alias entity-a"), []byte("explicit task")} {
		if bytesContains(physical, secret) {
			t.Fatalf("knowledge plaintext on disk: %q", secret)
		}
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e, found, err := s.GetKnowledgeEntity(ctx, "entity-a")
	if err != nil || !found || e.Facts[0].Sources[0].ID != repeatHex("a") {
		t.Fatalf("reopened entity=%+v found=%v err=%v", e, found, err)
	}
}

func TestKnowledgeAliasCollisionCorrectionMergeAndUnmerge(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x82), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := knowledge.Entity{ID: "a", Kind: knowledge.Person, Name: "Ada", Aliases: []string{"Ada Lovelace"}, State: knowledge.Confirmed}
	b := knowledge.Entity{ID: "b", Kind: knowledge.Person, Name: "Augusta", Aliases: []string{"Ada Lovelace"}, State: knowledge.Confirmed}
	if err := s.PutKnowledgeEntity(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeEntity(ctx, b); err == nil {
		t.Fatal("alias collision accepted")
	}
	b.Aliases = nil
	if err := s.PutKnowledgeEntity(ctx, b); err != nil {
		t.Fatalf("create after collision check: %v", err)
	}
	a.Name = "Ada Byron"
	a.Aliases = []string{"Ada Lovelace", "Countess of Lovelace"}
	if err := s.PutKnowledgeEntity(ctx, a); err != nil {
		t.Fatalf("correction: %v", err)
	}
	if _, ok, err := s.SearchKnowledgeExact(ctx, "Ada"); err != nil || ok {
		t.Fatalf("stale old exact key found=%v err=%v", ok, err)
	}
	if err := s.MergeKnowledge(ctx, "a", "b"); err != nil {
		t.Fatal(err)
	}
	merged, ok, err := s.GetKnowledgeEntity(ctx, "b")
	if err != nil || !ok || len(merged.Aliases) != 3 {
		t.Fatalf("merged=%+v found=%v err=%v", merged, ok, err)
	}
	merged.Name = "Augusta Byron"
	if err := s.PutKnowledgeEntity(ctx, merged); err != nil {
		t.Fatalf("edit merged target: %v", err)
	}
	if err := s.UnmergeKnowledge(ctx, "a", "b"); err == nil {
		t.Fatal("unmerge overwrote a post-merge target edit")
	}
	merged.Name = "Augusta"
	if err := s.PutKnowledgeEntity(ctx, merged); err != nil {
		t.Fatalf("restore merged projection: %v", err)
	}
	if err := s.UnmergeKnowledge(ctx, "a", "b"); err != nil {
		t.Fatal(err)
	}
	restored, ok, err := s.GetKnowledgeEntity(ctx, "a")
	if err != nil || !ok || restored.Name != "Ada Byron" {
		t.Fatalf("restored=%+v found=%v err=%v", restored, ok, err)
	}
}

func TestDeleteAllKnowledgeRemovesEntitiesAliasesAndMergeLineage(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x8a), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, entity := range []knowledge.Entity{
		{ID: "person-a", Kind: knowledge.Person, Name: "Ada", Aliases: []string{"Ada Lovelace"}, State: knowledge.Confirmed},
		{ID: "person-b", Kind: knowledge.Person, Name: "Augusta", State: knowledge.Confirmed},
		{ID: "project-c", Kind: knowledge.Project, Name: "Compiler", State: knowledge.Confirmed},
	} {
		if err := s.PutKnowledgeEntity(ctx, entity); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MergeKnowledge(ctx, "person-a", "person-b"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAllKnowledge(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListKnowledge(ctx, knowledge.PageRequest{Limit: knowledge.MaxPageSize})
	if err != nil || len(page.Entities) != 0 || page.HasMore {
		t.Fatalf("knowledge remains after delete all: %+v err=%v", page, err)
	}
	for _, name := range []string{"Ada", "Ada Lovelace", "Augusta", "Compiler"} {
		if _, found, err := s.SearchKnowledgeExact(ctx, name); err != nil || found {
			t.Fatalf("name %q remains indexed: found=%t err=%v", name, found, err)
		}
	}
	if err := s.PutKnowledgeEntity(ctx, knowledge.Entity{ID: "person-a", Kind: knowledge.Person, Name: "Ada", State: knowledge.Confirmed}); err != nil {
		t.Fatalf("recreate after delete all: %v", err)
	}
	if err := s.PutKnowledgeEntity(ctx, knowledge.Entity{ID: "person-b", Kind: knowledge.Person, Name: "Augusta", State: knowledge.Confirmed}); err != nil {
		t.Fatalf("recreate merge target after delete all: %v", err)
	}
	if err := s.UnmergeKnowledge(ctx, "person-a", "person-b"); err == nil {
		t.Fatal("merge lineage survived delete all")
	}
}

func TestDeleteAllKnowledgeWaitsForInFlightKnowledgeMutation(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x8b), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutKnowledgeEntity(ctx, knowledge.Entity{ID: "person", Kind: knowledge.Person, Name: "Ada", State: knowledge.Confirmed}); err != nil {
		t.Fatal(err)
	}

	// Hold the shared knowledge fence to model a mutation already in flight.
	s.state.knowledgeMu.RLock()
	deleteStarted := make(chan struct{})
	deleteDone := make(chan error, 1)
	go func() {
		close(deleteStarted)
		deleteDone <- s.DeleteAllKnowledge(ctx)
	}()
	<-deleteStarted
	select {
	case err := <-deleteDone:
		s.state.knowledgeMu.RUnlock()
		t.Fatalf("delete crossed an in-flight mutation fence: %v", err)
	default:
	}
	s.state.knowledgeMu.RUnlock()
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delete all did not proceed after mutation fence released")
	}
	page, err := s.ListKnowledge(ctx, knowledge.PageRequest{Limit: knowledge.MaxPageSize})
	if err != nil || len(page.Entities) != 0 {
		t.Fatalf("entity survived fenced delete: %+v err=%v", page, err)
	}
}

func TestKnowledgeMutationFailsClosedDuringDeleteFence(t *testing.T) {
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x8c), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.state.knowledgeMu.Lock()
	err = s.PutKnowledgeEntity(context.Background(), knowledge.Entity{ID: "late", Kind: knowledge.Person, Name: "Late", State: knowledge.Confirmed})
	s.state.knowledgeMu.Unlock()
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("mutation during delete fence error=%v, want ErrUnavailable", err)
	}
	if _, found, err := s.GetKnowledgeEntity(context.Background(), "late"); err != nil || found {
		t.Fatalf("fenced mutation persisted: found=%t err=%v", found, err)
	}
}

func TestKnowledgeSourceDeletionRemovesLinkedClaims(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x83), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := knowledge.Entity{ID: "project", Kind: knowledge.Project, Name: "Project", State: knowledge.Confirmed,
		Facts:     []knowledge.Fact{{ID: "f1", Text: "evidence-backed", State: knowledge.Deterministic, Sources: []knowledge.Source{{ID: repeatHex("b")}}}},
		OpenLoops: []knowledge.OpenLoop{{ID: "l1", Text: "follow up", State: knowledge.Proposed, Sources: []knowledge.Source{{ID: repeatHex("b")}}}}, ActivitySourceIDs: []string{repeatHex("b")}}
	if err := s.PutKnowledgeEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := s.QuarantineKnowledgeSource(ctx, repeatHex("b")); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetKnowledgeEntity(ctx, "project")
	if err != nil || !ok || len(got.Facts) != 0 || len(got.OpenLoops) != 0 || len(got.ActivitySourceIDs) != 0 {
		t.Fatalf("after source removal %+v, found=%v err=%v", got, ok, err)
	}
}

func TestDeleteObservationRecordAtomicallyQuarantinesKnowledgeClaims(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x89), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	source, err := capture.SanitizeObservation("editor.app", "docs.example", "source text", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservation(ctx, source); err != nil {
		t.Fatal(err)
	}
	e := knowledge.Entity{ID: "project", Kind: knowledge.Project, Name: "Project", State: knowledge.Confirmed,
		Facts:             []knowledge.Fact{{ID: "fact", Text: "source-derived fact", State: knowledge.Deterministic, Sources: []knowledge.Source{{ID: source.ContentID()}}}},
		OpenLoops:         []knowledge.OpenLoop{{ID: "loop", Text: "source-derived loop", State: knowledge.Proposed, Sources: []knowledge.Source{{ID: source.ContentID()}}}},
		ActivitySourceIDs: []string{source.ContentID()},
	}
	if err := s.PutKnowledgeEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteObservationRecord(ctx, source.ContentID()); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetKnowledgeEntity(ctx, e.ID)
	if err != nil || !found || len(got.Facts) != 0 || len(got.OpenLoops) != 0 || len(got.ActivitySourceIDs) != 0 {
		t.Fatalf("source-derived knowledge survived deletion: entity=%+v found=%t err=%v", got, found, err)
	}
}

func TestPutKnowledgeEntityFromSourceChecksLiveEvidenceAtomically(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x92), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	source, err := capture.SanitizeObservation("editor.app", "local", "Northstar is written in Go.", 1024)
	if err != nil {
		t.Fatal(err)
	}
	entity := knowledge.Entity{ID: "project-northstar", Kind: knowledge.Project, Name: "Northstar", State: knowledge.Confirmed,
		Facts: []knowledge.Fact{{ID: "fact-northstar", Text: "The project is written in Go.", State: knowledge.Confirmed, Sources: []knowledge.Source{{ID: source.ContentID()}}}},
	}
	if err := s.PutKnowledgeEntityFromSource(ctx, entity, source.ContentID()); err == nil {
		t.Fatal("entity referencing missing evidence was persisted")
	}
	if err := s.PutObservation(ctx, source); err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeEntityFromSource(ctx, entity, source.ContentID()); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteObservationRecord(ctx, source.ContentID()); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetKnowledgeEntity(ctx, entity.ID)
	if err != nil || !found || len(got.Facts) != 0 {
		t.Fatalf("source deletion did not cascade accepted fact: %+v found=%t err=%v", got, found, err)
	}
}

func TestPutKnowledgeEntityFromSourcePreservesExistingDuplicate(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x93), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	source, err := capture.SanitizeObservation("editor.app", "local", "Northstar is written in Go.", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservation(ctx, source); err != nil {
		t.Fatal(err)
	}
	existing := knowledge.Entity{ID: "project-original", Kind: knowledge.Project, Name: "Northstar", State: knowledge.Confirmed,
		Facts: []knowledge.Fact{{ID: "fact-original", Text: "Original user fact.", State: knowledge.Confirmed, Sources: []knowledge.Source{{UserAuthored: true}}}},
	}
	if err := s.PutKnowledgeEntity(ctx, existing); err != nil {
		t.Fatal(err)
	}
	duplicate := knowledge.Entity{ID: "project-suggested", Kind: knowledge.Project, Name: "Northstar", State: knowledge.Confirmed,
		Facts: []knowledge.Fact{{ID: "fact-suggested", Text: "The project is written in Go.", State: knowledge.Confirmed, Sources: []knowledge.Source{{ID: source.ContentID()}}}},
	}
	if err := s.PutKnowledgeEntityFromSource(ctx, duplicate, source.ContentID()); err == nil {
		t.Fatal("duplicate entity name was accepted")
	}
	got, found, err := s.GetKnowledgeEntity(ctx, existing.ID)
	if err != nil || !found || got.Facts[0].Text != "Original user fact." {
		t.Fatalf("existing entry changed: %+v found=%t err=%v", got, found, err)
	}
	if _, found, err := s.GetKnowledgeEntity(ctx, duplicate.ID); err != nil || found {
		t.Fatalf("duplicate was partly persisted: found=%t err=%v", found, err)
	}
}

func TestDeleteObservationRecordRollbackRestoresSourceAndKnowledge(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x90), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	source, err := capture.SanitizeObservation("editor.app", "docs.example", "source text", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservation(ctx, source); err != nil {
		t.Fatal(err)
	}
	e := knowledge.Entity{ID: "project", Kind: knowledge.Project, Name: "Project", State: knowledge.Confirmed,
		Facts: []knowledge.Fact{{ID: "fact", Text: "source-derived fact", State: knowledge.Deterministic, Sources: []knowledge.Source{{ID: source.ContentID()}}}},
	}
	if err := s.PutKnowledgeEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	err = s.Update(ctx, func(tx *Tx) error {
		if err := tx.deleteObservationRecord(source.ContentID()); err != nil {
			return err
		}
		return errors.New("force transaction rollback")
	})
	if !errors.Is(err, ErrTransaction) {
		t.Fatalf("rollback error = %v", err)
	}
	if _, found, err := s.GetObservation(ctx, source.ContentID()); err != nil || !found {
		t.Fatalf("source lost after rollback: found=%t err=%v", found, err)
	}
	got, found, err := s.GetKnowledgeEntity(ctx, e.ID)
	if err != nil || !found || len(got.Facts) != 1 || len(got.Facts[0].Sources) != 1 {
		t.Fatalf("knowledge changed after rollback: %+v found=%t err=%v", got, found, err)
	}
}

func TestKnowledgeDeleteRemovesEntityAndExactAliases(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0x84), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := knowledge.Entity{ID: "delete-me", Kind: knowledge.Topic, Name: "Ephemeral topic", Aliases: []string{"Temporary"}, State: knowledge.Confirmed}
	if err := s.PutKnowledgeEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteKnowledgeEntity(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.GetKnowledgeEntity(ctx, e.ID); err != nil || ok {
		t.Fatalf("deleted entity found=%v err=%v", ok, err)
	}
	if _, ok, err := s.SearchKnowledgeExact(ctx, "Temporary"); err != nil || ok {
		t.Fatalf("deleted alias found=%v err=%v", ok, err)
	}
}

func repeatHex(ch string) string                 { return strings.Repeat(ch, 64) }
func bytesContains(haystack, needle []byte) bool { return bytes.Contains(haystack, needle) }
