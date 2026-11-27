package durable

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/semantic"
	"github.com/hazcod/blur/internal/storage"
)

func TestSemanticProgressSourceDeletionRemovesEncryptedRecord(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xe1), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	document := semanticDocumentFixture(t, s, "A source whose successful vector batch is unfinished", "a")
	progress := semantic.Progress{Document: document}
	if err := s.PutSemanticProgress(ctx, progress); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.readIndexValue(ctx, semanticProgressNamespace, document.SourceID, semantic.MaxProgressBytes); err != nil || !found {
		t.Fatalf("progress record missing: found=%v err=%v", found, err)
	}
	if err := s.DeleteObservationRecord(ctx, document.SourceID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.readIndexValue(ctx, semanticProgressNamespace, document.SourceID, semantic.MaxProgressBytes); err != nil || found {
		t.Fatalf("erasure retained encrypted vector record: found=%v err=%v", found, err)
	}
	if err := s.PutSemanticProgress(ctx, progress); !errors.Is(err, semantic.ErrSourceGone) {
		t.Fatalf("late batch recreated erased progress: %v", err)
	}
}

func TestSemanticProgressPublishedPartErasureRemovesEncryptedRecord(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xe2), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	publishIndexedFixture(t, s, "An unpublished vector prefix for a published capture part.", time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC))
	drainCaptureIndex(t, s)
	page, err := s.ListCaptureEvidencePage(ctx, storage.CaptureEvidencePageRequest{Limit: 4, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Sources) != 1 {
		t.Fatal(err)
	}
	source := page.Sources[0]
	document := semantic.Document{SourceID: source.SourceID(), Model: semantic.Model{Name: "fixture:latest", Digest: strings.Repeat("a", 64), Dimensions: 2, Format: semantic.FormatPlain}, AppID: source.AppID(), Domain: source.Domain(), CapturedAt: page.CapturedAt[0], TextBytes: len(source.Text()), Chunks: []semantic.Chunk{{Start: 0, End: len(source.Text()), Vector: []float32{1, 0}}}}
	progress := semantic.Progress{Document: document}
	if err := s.PutSemanticProgress(ctx, progress); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteCapturesBefore(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.readIndexValue(ctx, semanticProgressNamespace, document.SourceID, semantic.MaxProgressBytes); err != nil || found {
		t.Fatalf("capture erasure retained vector prefix: found=%v err=%v", found, err)
	}
	if err := s.PutSemanticProgress(ctx, progress); !errors.Is(err, semantic.ErrSourceGone) {
		t.Fatalf("late capture batch recreated erased progress: %v", err)
	}
}

func TestSemanticProgressRejectsSupersededNoteRevisionAndWrongProvenance(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xe3), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	note, err := s.CreateNoteDocument(ctx, "First revision awaiting completion.", at)
	if err != nil {
		t.Fatal(err)
	}
	source, found, err := s.GetObservation(ctx, note.SourceID)
	if err != nil || !found {
		t.Fatal(err)
	}
	document := semantic.Document{SourceID: note.SourceID, Model: semantic.Model{Name: "fixture:latest", Digest: strings.Repeat("a", 64), Dimensions: 2, Format: semantic.FormatPlain}, AppID: source.AppID(), Domain: source.Domain(), CapturedAt: at, TextBytes: len(note.Text), Chunks: []semantic.Chunk{{Start: 0, End: len(note.Text), Vector: []float32{1, 0}}}}
	progress := semantic.Progress{Document: document}
	if err := s.PutSemanticProgress(ctx, progress); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*semantic.Document){
		func(d *semantic.Document) { d.CapturedAt = d.CapturedAt.Add(time.Nanosecond) },
		func(d *semantic.Document) { d.AppID = "other.app" },
		func(d *semantic.Document) { d.Domain = "other.example" },
		func(d *semantic.Document) { d.TextBytes++ },
		func(d *semantic.Document) { d.Chunks[0].End-- },
	} {
		wrong := document.Clone()
		mutate(&wrong)
		if err := s.PutSemanticProgress(ctx, semantic.Progress{Document: wrong}); err == nil {
			t.Fatal("progress accepted mismatching source context")
		}
	}
	if _, err := s.UpdateNoteDocument(ctx, note.ID, 1, "Second revision is current.", at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetSemanticProgress(ctx, note.SourceID, document.Model.Key()); err != nil || found {
		t.Fatalf("superseded revision retained usable progress: found=%v err=%v", found, err)
	}
	if err := s.PutSemanticProgress(ctx, progress); !errors.Is(err, semantic.ErrSourceGone) {
		t.Fatalf("late note batch recreated superseded progress: %v", err)
	}
}
