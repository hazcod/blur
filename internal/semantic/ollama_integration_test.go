package semantic

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLocalOllamaSemanticRecallWithoutSharedWords(t *testing.T) {
	if os.Getenv("BLUR_EMBEDDING_INTEGRATION") != "1" {
		t.Skip("set BLUR_EMBEDDING_INTEGRATION=1 to verify the local embedding model")
	}
	embedder, err := NewOllamaEmbedder("http://127.0.0.1:11434", DefaultEmbeddingModel, DefaultDimensions)
	if err != nil {
		t.Fatal(err)
	}
	defer embedder.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	identity, err := embedder.Identity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{
		"A reservation for two at Le Bernardin on Friday evening.",
		"The Go compiler reports an unused variable in main.go.",
		"We compared encryption keys and database deletion semantics.",
	}
	vectors, err := embedder.EmbedDocuments(ctx, identity, texts)
	if err != nil {
		t.Fatal(err)
	}
	index, err := NewIndex(identity)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Purge()
	for number, text := range texts {
		id := strings.Repeat(string(rune('a'+number)), 64)
		document := Document{SourceID: id, Model: identity, AppID: "reader.app", Domain: "research.example", CapturedAt: time.Now().UTC(), TextBytes: len(text), Chunks: []Chunk{{Start: 0, End: len(text), Vector: vectors[number]}}}
		if err := index.AddDocument(ctx, document); err != nil {
			t.Fatal(err)
		}
	}
	for _, question := range []string{"Which restaurant did I book for dinner?", "Waar wilde ik vrijdag gaan eten?"} {
		vector, err := embedder.EmbedQuery(ctx, identity, question)
		if err != nil {
			t.Fatal(err)
		}
		hits, err := index.Search(ctx, Query{Vector: vector, Limit: 3})
		if err != nil || len(hits) == 0 || hits[0].SourceID != strings.Repeat("a", 64) {
			t.Fatalf("paraphrase/cross-language recall failed for %q: %+v err=%v", question, hits, err)
		}
		t.Logf("local semantic recall %q: best cosine %.4f; %d candidates", question, hits[0].Score, len(hits))
	}
}
