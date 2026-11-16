package semantic

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func fixtureModel() Model {
	return Model{Name: "embeddinggemma:latest", Digest: strings.Repeat("d", 64), Dimensions: 2, Format: FormatGemma}
}

func fixtureDocument(id, app string, at time.Time, vector []float32) Document {
	return Document{SourceID: strings.Repeat(id, 64), Model: fixtureModel(), AppID: app, Domain: "work.example", CapturedAt: at, TextBytes: 20, Chunks: []Chunk{{Start: 0, End: 20, Vector: vector}}}
}

func TestSemanticScopeRequiresBothAppAndDomainBeforeRanking(t *testing.T) {
	ctx := context.Background()
	index, err := NewIndex(fixtureModel())
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	want := fixtureDocument("a", "reader.app", at, []float32{.8, .6})
	otherSite := fixtureDocument("b", "reader.app", at, []float32{1, 0})
	otherSite.Domain = "other.example"
	otherApp := fixtureDocument("c", "editor.app", at, []float32{1, 0})
	for _, doc := range []Document{want, otherSite, otherApp} {
		if err := index.AddDocument(ctx, doc); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := index.Search(ctx, Query{Vector: []float32{1, 0}, Limit: 1, AppIDs: []string{"reader.app"}, Domains: []string{"work.example"}})
	if err != nil || len(hits) != 1 || hits[0].SourceID != want.SourceID {
		t.Fatalf("app/site qualifiers widened before ranking: %+v %v", hits, err)
	}
}

func TestSplitTextCoversLargeUnicodeSourceWithoutBrokenBoundaries(t *testing.T) {
	text := strings.Repeat("Een café ☕ reviewed the deployment plan.\n", 20000)
	spans, err := SplitText(text)
	if err != nil || len(spans) < 2 || len(spans) > MaxChunksPerSource {
		t.Fatalf("split count=%d err=%v", len(spans), err)
	}
	covered := 0
	for _, span := range spans {
		if span.Start > covered || span.End <= span.Start || span.End-span.Start > ChunkBytes || !utf8.ValidString(text[span.Start:span.End]) {
			t.Fatalf("invalid or missing source coverage at %+v after %d bytes", span, covered)
		}
		covered = span.End
	}
	if covered != len(text) {
		t.Fatalf("covered %d of %d source bytes", covered, len(text))
	}
}

func TestSplitTextCoversMaximumSourceWithSparseWhitespace(t *testing.T) {
	text := strings.Repeat(strings.Repeat("a", ChunkBytes-192)+" ", 1000)[:1<<20]
	spans, err := SplitText(text)
	if err != nil || len(spans) == 0 || spans[len(spans)-1].End != len(text) {
		t.Fatalf("maximum source was omitted: chunks=%d err=%v", len(spans), err)
	}
}

func TestSemanticDocumentCodecPreservesVectorBitsAndRejectsTrailingData(t *testing.T) {
	document := fixtureDocument("a", "reader.app", time.Now().UTC(), []float32{0.8, 0.6})
	encoded, err := EncodeDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(encoded)
	got, err := DecodeDocument(encoded)
	if err != nil || got.SourceID != document.SourceID || got.Model.Key() != document.Model.Key() || len(got.Chunks) != 1 || math.Float32bits(got.Chunks[0].Vector[0]) != math.Float32bits(document.Chunks[0].Vector[0]) {
		t.Fatalf("codec round-trip failed: %+v err=%v", got, err)
	}
	if _, err := DecodeDocument(append(encoded, 0)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("trailing data accepted: %v", err)
	}
}

func TestSemanticIndexFiltersBeforeRankingAndHonorsLaterSightings(t *testing.T) {
	ctx := context.Background()
	index, err := NewIndex(fixtureModel())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	old := fixtureDocument("a", "reader.app", start.Add(-24*time.Hour), []float32{1, 0})
	recent := fixtureDocument("b", "reader.app", start.Add(time.Hour), []float32{0.8, 0.6})
	other := fixtureDocument("c", "chat.app", start.Add(2*time.Hour), []float32{1, 0})
	for _, document := range []Document{old, recent, other} {
		if err := index.AddDocument(ctx, document); err != nil {
			t.Fatal(err)
		}
	}
	query := Query{Vector: []float32{1, 0}, Limit: 1, AppIDs: []string{"reader.app"}, Start: start, End: start.AddDate(0, 0, 1)}
	hits, err := index.Search(ctx, query)
	if err != nil || len(hits) != 1 || hits[0].SourceID != recent.SourceID {
		t.Fatalf("scoped semantic matches=%+v err=%v", hits, err)
	}
	query.ScopeSourceIDs = []string{old.SourceID}
	hits, err = index.Search(ctx, query)
	if err != nil || len(hits) != 1 || hits[0].SourceID != old.SourceID {
		t.Fatalf("later-sighting semantic match=%+v err=%v", hits, err)
	}
}

func TestSemanticIndexCopiesVectorsAndRemovesDeletedEvidence(t *testing.T) {
	ctx := context.Background()
	index, _ := NewIndex(fixtureModel())
	document := fixtureDocument("a", "reader.app", time.Now(), []float32{1, 0})
	if err := index.AddDocument(ctx, document); err != nil {
		t.Fatal(err)
	}
	document.Chunks[0].Vector[0] = 0
	hits, err := index.Search(ctx, Query{Vector: []float32{1, 0}, Limit: 8})
	if err != nil || len(hits) != 1 || hits[0].Score < 0.99 {
		t.Fatalf("caller mutated retained embeddings: %+v err=%v", hits, err)
	}
	index.Remove(document.SourceID)
	hits, err = index.Search(ctx, Query{Vector: []float32{1, 0}, Limit: 8})
	if err != nil || len(hits) != 0 {
		t.Fatalf("deleted evidence remained searchable: %+v err=%v", hits, err)
	}
}

func TestSemanticDocumentsRejectIncompatibleOrInvalidVectors(t *testing.T) {
	index, _ := NewIndex(fixtureModel())
	for _, vector := range [][]float32{{0, 0}, {1}, {float32(math.NaN()), 0}, {float32(math.Inf(1)), 0}} {
		document := fixtureDocument("a", "reader.app", time.Now(), vector)
		if err := index.AddDocument(context.Background(), document); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid vector accepted: err=%v", err)
		}
	}
	document := fixtureDocument("a", "reader.app", time.Now(), []float32{1, 0})
	document.Model.Digest = strings.Repeat("e", 64)
	if err := index.AddDocument(context.Background(), document); !errors.Is(err, ErrModelMismatch) {
		t.Fatalf("incompatible model generation accepted: err=%v", err)
	}
}

func TestSemanticIndexCanReplaceAndReinsertItsLastDocument(t *testing.T) {
	ctx := context.Background()
	index, _ := NewIndex(fixtureModel())
	document := fixtureDocument("a", "reader.app", time.Now(), []float32{1, 0})
	if err := index.AddDocument(ctx, document); err != nil {
		t.Fatal(err)
	}
	document.Chunks[0].Vector = []float32{0, 1}
	if err := index.AddDocument(ctx, document); err != nil {
		t.Fatal(err)
	}
	index.Remove(document.SourceID)
	if err := index.AddDocument(ctx, document); err != nil {
		t.Fatal(err)
	}
	hits, err := index.Search(ctx, Query{Vector: []float32{0, 1}, Limit: 1})
	if err != nil || len(hits) != 1 || hits[0].Score < .99 {
		t.Fatalf("reinserted source=%+v err=%v", hits, err)
	}
}
