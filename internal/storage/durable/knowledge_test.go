package durable

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/nielsgroen/blur/internal/knowledge"
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
