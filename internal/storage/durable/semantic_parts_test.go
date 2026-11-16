package durable

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/semantic"
	"github.com/hazcod/blur/internal/storage"
)

func TestSemanticPublishedPartReloadAndSourceErasure(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xdd), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	publishIndexedFixture(t, s, "A reservation for the Atlas project dinner.", time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC))
	drainCaptureIndex(t, s)
	page, err := s.ListCaptureEvidencePage(ctx, storage.CaptureEvidencePageRequest{Limit: 4, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Sources) != 1 {
		t.Fatal(err)
	}
	source := page.Sources[0]
	doc := semantic.Document{SourceID: source.SourceID(), Model: semantic.Model{Name: "synthetic:latest", Digest: strings.Repeat("a", 64), Dimensions: 2, Format: semantic.FormatPlain}, AppID: source.AppID(), Domain: source.Domain(), CapturedAt: page.CapturedAt[0], TextBytes: len(source.Text()), Chunks: []semantic.Chunk{{Start: 0, End: len(source.Text()), Vector: []float32{1, 0}}}}
	if err := s.PutSemanticDocument(ctx, doc); err != nil {
		t.Fatal("part source validator rejected exact provenance", err)
	}
	for _, mutate := range []func(*semantic.Document){
		func(d *semantic.Document) { d.CapturedAt = d.CapturedAt.Add(time.Nanosecond) },
		func(d *semantic.Document) { d.AppID = "other.app" },
		func(d *semantic.Document) { d.Domain = "other.example" },
		func(d *semantic.Document) { p, _ := source.Provenance(); d.SourceID = p.EditionID },
	} {
		wrong := doc.Clone()
		mutate(&wrong)
		if err := s.PutSemanticDocument(ctx, wrong); err == nil {
			t.Fatal("part semantic source authority was weakened")
		}
	}
	if got, found, err := s.GetSemanticDocument(ctx, source.SourceID()); err != nil || !found || got.SourceID != source.SourceID() {
		t.Fatal("part semantic document could not reload", err)
	}
	if _, err := s.DeleteCapturesBefore(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetSemanticDocument(ctx, source.SourceID()); err != nil || found {
		t.Fatal("capture deletion retained part vectors", err)
	}
	if err := s.PutSemanticDocument(ctx, doc); !errors.Is(err, semantic.ErrSourceGone) {
		t.Fatal("late vector publication recreated erased source", err)
	}
}

func TestSemanticCaptureCheckpointPreservesLegacyBytesAndBounds(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xde), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	key := strings.Repeat("a", 64)
	checkpoint := semantic.Checkpoint{ModelKey: key, Complete: true}
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"model_key":"`+key+`","indexed":0,"skipped":0,"complete":true}` {
		t.Fatal("zero optional fields changed authenticated legacy checkpoint bytes")
	}
	checkpoint.CaptureAfter = strings.Repeat("b", 64) + ":" + strings.Repeat("c", 64) + ":9223372036854775807"
	encoded, err = json.Marshal(checkpoint)
	if err != nil || len(encoded) > 512 {
		t.Fatal("capture cursor exceeded authenticated checkpoint envelope", err)
	}
	if err := s.PutSemanticCheckpoint(ctx, checkpoint); err != nil {
		t.Fatal(err)
	}
	if got, found, err := s.GetSemanticCheckpoint(ctx, key); err != nil || !found || got != checkpoint {
		t.Fatal("capture cursor roundtrip lost provenance", err)
	}
	checkpoint.CaptureAfter += "0"
	if err := s.PutSemanticCheckpoint(ctx, checkpoint); err == nil {
		t.Fatal("invalid capture cursor accepted")
	}
}
