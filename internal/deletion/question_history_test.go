package deletion

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
)

func questionFixture(t *testing.T) (*durable.Store, *Service, []string) {
	t.Helper()
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "blur.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var ids []string
	for i, text := range []string{"First cited private source text.", "Second cited private source text."} {
		o, err := capture.SanitizeObservation("editor", "", text, 128)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PutObservationAt(ctx, o, time.Date(2026, 11, 15, 9+i, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, o.ContentID())
	}
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	for i, sources := range [][]string{{ids[0], ids[1]}, {ids[0]}, {ids[1]}} {
		if err := store.AppendQuestion(ctx, storage.QuestionRecord{Question: []string{"one", "two", "three"}[i], AskedAt: at.Add(time.Duration(i) * time.Minute), PlanKind: "lookup", SourceIDs: sources}); err != nil {
			t.Fatal(err)
		}
	}
	service, err := NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	return store, service, ids
}

func TestDeleteSourceRemovesItFromQuestionHistory(t *testing.T) {
	ctx := context.Background()
	store, service, ids := questionFixture(t)
	if _, err := service.DeleteSource(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListQuestions(ctx, 10)
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	for _, row := range rows {
		if slices.Contains(row.SourceIDs, ids[0]) {
			t.Fatalf("%q still cites the deleted source", row.Question)
		}
	}
	if !slices.Equal(rows[0].SourceIDs, []string{ids[1]}) || !slices.Equal(rows[2].SourceIDs, []string{ids[1]}) {
		t.Fatalf("unrelated citations were disturbed: %+v", rows)
	}
}

func TestDeleteBeforeRemovesExpiredSourcesFromQuestionHistory(t *testing.T) {
	ctx := context.Background()
	store, service, ids := questionFixture(t)
	if _, err := service.DeleteBefore(ctx, time.Date(2026, 11, 15, 9, 30, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	rows, _ := store.ListQuestions(ctx, 10)
	for _, row := range rows {
		if slices.Contains(row.SourceIDs, ids[0]) {
			t.Fatalf("%q still cites an expired source", row.Question)
		}
	}
	if len(rows) != 3 || !slices.Equal(rows[2].SourceIDs, []string{ids[1]}) {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestDeleteAllClearsQuestionHistory(t *testing.T) {
	ctx := context.Background()
	store, service, _ := questionFixture(t)
	if _, err := service.DeleteAll(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListQuestions(ctx, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("question history survived delete-all: %d rows err=%v", len(rows), err)
	}
}
