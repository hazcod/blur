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
	"github.com/hazcod/blur/internal/semantic"
)

func semanticDocumentFixture(t *testing.T, store *Store, text string, modelDigest string) semantic.Document {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	source, err := capture.SanitizeObservation("editor.app", "work.example", text, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, source, at); err != nil {
		t.Fatal(err)
	}
	return semantic.Document{SourceID: source.ContentID(), AppID: source.AppID(), Domain: source.Domain(), CapturedAt: at, TextBytes: len(source.Text()), Model: semantic.Model{Name: "private-embedding-fixture:latest", Digest: strings.Repeat(modelDigest, 64), Dimensions: 2, Format: semantic.FormatPlain}, Chunks: []semantic.Chunk{{Start: 0, End: len(source.Text()), Vector: []float32{0.8, 0.6}}}}
}

func TestSemanticVectorsAreEncryptedAndDeletedAtomicallyWithTheirSource(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xb1)
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	document := semanticDocumentFixture(t, store, "A source with semantic memory provenance", "a")
	if err := store.PutSemanticDocument(ctx, document); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSemanticCheckpoint(ctx, semantic.Checkpoint{ModelKey: document.Model.Key(), After: document.SourceID, Indexed: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(document.Model.Name)) || bytes.Contains(raw, []byte(document.SourceID)) {
		t.Fatal("semantic model/source metadata was persisted in plaintext")
	}
	clear(raw)
	store, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := store.GetSemanticDocument(ctx, document.SourceID)
	if err != nil || !found || got.Model.Key() != document.Model.Key() || len(got.Chunks) != 1 || got.Chunks[0].Vector[0] != 0.8 {
		t.Fatalf("semantic document reopen: found=%v doc=%+v err=%v", found, got, err)
	}
	if err := store.DeleteObservationRecord(ctx, document.SourceID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetSemanticDocument(ctx, document.SourceID); err != nil || found {
		t.Fatalf("deleted vectors remained reachable: found=%v err=%v", found, err)
	}
	page, err := store.ListSemanticDocuments(ctx, semantic.PageRequest{ModelKey: document.Model.Key(), Limit: 8, MaxBytes: semantic.MaxPageBytes})
	if err != nil || len(page.Documents) != 0 || page.HasMore {
		t.Fatalf("semantic directory retained deleted source: %+v err=%v", page, err)
	}
	checkpoint, found, err := store.GetSemanticCheckpoint(ctx, document.Model.Key())
	if err != nil || !found || checkpoint.After != "" || checkpoint.Complete {
		t.Fatalf("checkpoint retained deleted source cursor: %+v err=%v", checkpoint, err)
	}
	if err := store.PutSemanticDocument(ctx, document); !errors.Is(err, semantic.ErrSourceGone) {
		t.Fatalf("late indexing resurrected deleted source: %v", err)
	}
}

func TestSemanticDirectoryModelFilterHasStableContinuation(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xb2), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	var modelKey string
	for index, text := range []string{"source alpha", "source beta", "source gamma"} {
		digest := "a"
		if index == 1 {
			digest = "b"
		}
		document := semanticDocumentFixture(t, store, text, digest)
		if err := store.PutSemanticDocument(ctx, document); err != nil {
			t.Fatal(err)
		}
		if digest == "a" {
			modelKey = document.Model.Key()
		}
	}
	request := semantic.PageRequest{ModelKey: modelKey, Limit: 1, MaxBytes: semantic.MaxPageBytes}
	seen := make(map[string]bool)
	for pageNumber := 0; pageNumber < 8; pageNumber++ {
		page, err := store.ListSemanticDocuments(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		for _, document := range page.Documents {
			if document.Model.Key() != modelKey || seen[document.SourceID] {
				t.Fatal("semantic generation/cursor mixed records")
			}
			seen[document.SourceID] = true
		}
		if !page.HasMore {
			break
		}
		if page.Next == "" || page.Next == request.After {
			t.Fatal("filtered semantic page did not advance")
		}
		request.After = page.Next
	}
	if len(seen) != 2 {
		t.Fatalf("loaded %d records for the selected generation, want 2", len(seen))
	}
}
