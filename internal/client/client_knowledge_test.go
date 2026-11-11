package client

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestAddKnowledgePersistsOnlyEncryptedUserEnteredContent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "memory.db")
	store, err := durable.OpenDurable(path, securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(store)
	if !client.CanAddKnowledge() {
		t.Fatal("durable store is not exposed as a knowledge writer")
	}
	const name = "AuroraPrivateProject"
	const note = "private library detail sentinel"
	entity, err := client.AddKnowledge(ctx, knowledge.Project, name, note)
	if err != nil {
		t.Fatal(err)
	}
	match, found, err := store.SearchKnowledgeExact(ctx, name)
	if err != nil || !found || match.ID != entity.ID {
		t.Fatalf("created entity is not searchable: found=%t err=%v", found, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	physical, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range [][]byte{[]byte(name), []byte(note)} {
		if bytes.Contains(physical, secret) {
			t.Fatalf("user-entered library content appeared in the database file: %q", secret)
		}
	}
}

func TestDeleteKnowledgeRemovesOnlyTheSelectedLibraryEntity(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := NewClient(store)
	entity, err := client.AddKnowledge(ctx, knowledge.Project, "Project Aurora", "user-authored detail")
	if err != nil {
		t.Fatal(err)
	}
	other, err := client.AddKnowledge(ctx, knowledge.Topic, "Project Borealis", "unrelated library detail")
	if err != nil {
		t.Fatal(err)
	}
	observation, err := capture.SanitizeObservation("com.example.editor", "local", "captured source", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservation(ctx, observation); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteKnowledge(ctx, entity.ID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetKnowledgeEntity(ctx, entity.ID); err != nil || found {
		t.Fatalf("deleted entity found=%t err=%v", found, err)
	}
	if _, found, err := store.SearchKnowledgeExact(ctx, "Project Aurora"); err != nil || found {
		t.Fatalf("deleted exact alias found=%t err=%v", found, err)
	}
	if _, found, err := store.GetKnowledgeEntity(ctx, other.ID); err != nil || !found {
		t.Fatalf("unrelated entity found=%t err=%v", found, err)
	}
	if _, found, err := store.GetObservation(ctx, observation.ContentID()); err != nil || !found {
		t.Fatalf("captured source was deleted: found=%t err=%v", found, err)
	}
}

func TestUnmergeKnowledgeRestoresBothLibraryEntries(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := NewClient(store)
	source, err := client.AddKnowledge(ctx, knowledge.Person, "Ada Lovelace", "works on analysis")
	if err != nil {
		t.Fatal(err)
	}
	target, err := client.AddKnowledge(ctx, knowledge.Person, "Augusta Ada", "original target detail")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MergeKnowledge(ctx, source.ID, target.ID); err != nil {
		t.Fatal(err)
	}
	if err := client.UnmergeKnowledge(ctx, source.ID, target.ID); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []knowledge.Entity{source, target} {
		actual, found, err := store.GetKnowledgeEntity(ctx, expected.ID)
		if err != nil || !found || actual.Name != expected.Name || actual.State != knowledge.Confirmed || actual.MergedInto != "" {
			t.Fatalf("restored entity=%+v found=%t err=%v", actual, found, err)
		}
	}
}
