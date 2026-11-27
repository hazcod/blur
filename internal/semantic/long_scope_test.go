package semantic_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/semantic"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
)

type longSemanticSightingProbe struct {
	*durable.Store
	calls  int
	cancel context.CancelFunc
}

func (s *longSemanticSightingProbe) SourceEvidenceSightingAt(ctx context.Context, id string, start, end time.Time) (time.Time, bool, error) {
	if end.Sub(start) > storage.MaxObservationWindow {
		return time.Time{}, false, semantic.ErrInvalid
	}
	s.calls++
	if s.cancel != nil && s.calls == 2 {
		s.cancel()
	}
	return s.Store.ObservationSightingAt(ctx, id, start, end)
}

func TestSemanticScopeDrainsTenYearsOfBoundedSightingReads(t *testing.T) {
	ctx := context.Background()
	store := serviceStore(t)
	start, end := time.Date(2016, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	visit := start.AddDate(0, 1, 0)
	o, err := capture.SanitizeObservation("reader.app", "work.example", "The archived Iris invoice documents a supplier correction.", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, o, start.AddDate(-1, 0, 0)); err != nil {
		t.Fatal(err)
	}
	probe := &longSemanticSightingProbe{Store: store}
	e := &serviceEmbedder{}
	s, err := semantic.NewService(probe, store, e, semantic.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Start()
	waitServiceReady(t, s, 1)
	if err := store.RecordObservationSighting(ctx, o, visit); err != nil {
		t.Fatal(err)
	}
	query := semantic.Query{Limit: 4, Start: start, End: end, AppIDs: []string{"reader.app"}, Domains: []string{"work.example"}}
	hits, err := s.Search(ctx, "supplier correction", query)
	if err != nil || len(hits) != 1 || hits[0].SourceID != o.ContentID() || !hits[0].CapturedAt.Equal(visit) || probe.calls < 100 {
		t.Fatalf("long semantic scope failed before its earliest relevant visit: %+v calls=%d err=%v", hits, probe.calls, err)
	}
	if e.documents.Load() != 1 {
		t.Fatal("query reembedded unchanged archived content")
	}
	probe.calls = 0
	queryCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	probe.cancel = cancel
	hits, err = s.Search(queryCtx, "supplier correction", query)
	if !errors.Is(err, context.Canceled) || len(hits) != 0 {
		t.Fatalf("cancelled semantic walk returned a successful empty/prefix result: %+v %v", hits, err)
	}
}
