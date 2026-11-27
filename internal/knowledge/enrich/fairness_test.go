package enrich_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
)

func fairnessStore(t *testing.T) *durable.Store {
	t.Helper()
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func fairnessCalls(g *compactingTestGenerator) int {
	n := 0
	for _, spans := range g.inputs {
		n += len(spans)
	}
	return n
}

// Legacy adapters deliberately do not expose a multipart source directory.
type fairnessLegacySources struct{ store *durable.Store }

func (s fairnessLegacySources) ListObservationPage(ctx context.Context, request storage.ObservationPageRequest) (storage.ObservationPage, error) {
	return s.store.ListObservationPage(ctx, request)
}
func (s fairnessLegacySources) GetObservationWithCaptureTime(ctx context.Context, id string) (capture.SanitizedObservation, time.Time, bool, error) {
	return s.store.GetObservationWithCaptureTime(ctx, id)
}

func TestCompactionUsesIdleQuantumForLegacyOnlyPendingWork(t *testing.T) {
	ctx := context.Background()
	s := fairnessStore(t)
	g := &compactingTestGenerator{allCaptureGenerator: allCaptureGenerator{inputs: map[string][]string{}}, last: map[string]string{}}
	w, err := enrich.NewService(fairnessLegacySources{store: s}, s, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	complete(t, w)
	for i := range 6 {
		o := source(t, s, fmt.Sprintf("Independent newly queued source %d.", i))
		w.NotifySource(o.ContentID())
	}
	if err := w.Backfill(ctx); !errors.Is(err, enrich.ErrMoreWork) {
		t.Fatal(err)
	}
	if fairnessCalls(g) != 4 || w.Status().Queued != 2 {
		t.Fatal("absent multipart directory consumed useful pending capacity", fairnessCalls(g), w.Status())
	}
}

func TestCompactionRotatesLongPendingSourcesWithinBoundedQuanta(t *testing.T) {
	ctx := context.Background()
	s := fairnessStore(t)
	var ids []string
	for i := range 4 {
		o := source(t, s, fmt.Sprintf("Agent %d source. ", i)+strings.Repeat("A long independent work stream needs resumable processing. ", 200))
		ids = append(ids, o.ContentID())
	}
	sort.Strings(ids)
	g := &compactingTestGenerator{allCaptureGenerator: allCaptureGenerator{inputs: map[string][]string{}}, last: map[string]string{}}
	w, err := enrich.NewService(s, s, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for _, id := range ids {
		w.NotifySource(id)
	}
	for pass := range 2 {
		before := fairnessCalls(g)
		if err := w.Backfill(ctx); !errors.Is(err, enrich.ErrMoreWork) {
			t.Fatalf("pass %d did not retain long-source work: %v", pass, err)
		}
		if calls := fairnessCalls(g) - before; calls > 4 {
			t.Fatalf("pass %d exceeded shared four-call quantum: %d", pass, calls)
		}
	}
	for _, id := range ids {
		if len(g.inputs[id]) == 0 {
			t.Fatalf("pending source %s starved behind first large capture", id)
		}
	}
	if w.Status().Complete {
		t.Fatal("fair slices falsely completed the pending backlog")
	}
}

func TestCompactionVisitsPublishedPartsWhileLegacyBackfillIsUnfinished(t *testing.T) {
	ctx := context.Background()
	s := fairnessStore(t)
	legacy := source(t, s, strings.Repeat("Long legacy work must not block newly published capture text. ", 200))
	publishedLibraryCapture(t, s, "New agent capture has an important completed build result.")
	parts, err := s.ListCaptureEvidencePage(ctx, storage.CaptureEvidencePageRequest{Limit: 4, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(parts.Sources) != 1 {
		t.Fatalf("published fixture missing: %+v %v", parts, err)
	}
	g := &compactingTestGenerator{allCaptureGenerator: allCaptureGenerator{inputs: map[string][]string{}}, last: map[string]string{}}
	w, err := enrich.NewService(s, s, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.Backfill(ctx); !errors.Is(err, enrich.ErrMoreWork) {
		t.Fatal(err)
	}
	if len(g.inputs[parts.Sources[0].SourceID()]) == 0 {
		t.Fatal("published capture starved behind incomplete legacy source")
	}
	if len(g.inputs[legacy.ContentID()]) == 0 {
		t.Fatal("published capture consumed legacy backfill's entire quantum")
	}
	if fairnessCalls(g) > 4 {
		t.Fatal("directories did not share the four-call quantum")
	}
	checkpoint, _, err := s.GetEnrichmentCheckpoint(ctx)
	if err != nil || checkpoint.Complete {
		t.Fatalf("partial legacy source falsely marked complete: %+v %v", checkpoint, err)
	}
}

func TestCompactionReservesBothDirectoriesUnderPendingCaptureLoad(t *testing.T) {
	ctx := context.Background()
	s := fairnessStore(t)
	var ids []string
	for i := range 4 {
		o := source(t, s, fmt.Sprintf("Independent source %d. ", i)+strings.Repeat("An accepted capture has substantial work still queued. ", 150))
		ids = append(ids, o.ContentID())
	}
	sort.Strings(ids)
	legacyPage, err := s.ListObservationPage(ctx, storage.ObservationPageRequest{Limit: 1, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(legacyPage.Observations) != 1 {
		t.Fatalf("legacy fixture missing: %+v %v", legacyPage, err)
	}
	legacyID := legacyPage.Observations[0].ContentID()
	publishedLibraryCapture(t, s, strings.Repeat("A published capture also has a long work backlog. ", 100))
	parts, err := s.ListCaptureEvidencePage(ctx, storage.CaptureEvidencePageRequest{Limit: 4, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(parts.Sources) != 1 {
		t.Fatalf("published fixture missing: %+v %v", parts, err)
	}
	g := &compactingTestGenerator{allCaptureGenerator: allCaptureGenerator{inputs: map[string][]string{}}, last: map[string]string{}}
	w, err := enrich.NewService(s, s, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	// Keep the first legacy source outside the incremental queue, so that
	// its progress proves that neither newer lane consumed the whole pass.
	for _, id := range ids {
		if id != legacyID {
			w.NotifySource(id)
		}
	}
	for pass := range 2 {
		before := fairnessCalls(g)
		if err := w.Backfill(ctx); !errors.Is(err, enrich.ErrMoreWork) {
			t.Fatal(err)
		}
		if calls := fairnessCalls(g) - before; calls != 4 {
			t.Fatalf("three work lanes did not share the four-call quantum: %d", calls)
		}
		if len(g.inputs[legacyID]) < pass+1 || len(g.inputs[parts.Sources[0].SourceID()]) < pass+1 {
			t.Fatal("pending sources starved a durable directory")
		}
	}
	for _, id := range ids {
		if id != legacyID && len(g.inputs[id]) == 0 {
			t.Fatal("pending source did not rotate into its turn")
		}
	}
}

func TestCompactionCaptureSlicePreservesLegacyInvalidRepairAdmission(t *testing.T) {
	ctx := context.Background()
	s := fairnessStore(t)
	source(t, s, strings.Repeat("Legacy processing remains unfinished after a single quantum. ", 1000))
	publishedLibraryCapture(t, s, "A short published capture finishes in one request.")
	g := &compactingTestGenerator{allCaptureGenerator: allCaptureGenerator{inputs: map[string][]string{}}, last: map[string]string{}}
	checkpoint := enrich.Checkpoint{ModelKey: enrich.ModelKey("compaction-test-v1"), OutputRevision: enrich.OutputDecoderRevision, RetryInvalid: true}
	if err := s.PutEnrichmentCheckpoint(ctx, checkpoint); err != nil {
		t.Fatal(err)
	}
	w, err := enrich.NewService(s, s, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	// A consumed one-call slice checkpoints its final row before a later
	// no-work tail check confirms the capture directory is complete.
	for range 2 {
		if err := w.Backfill(ctx); !errors.Is(err, enrich.ErrMoreWork) {
			t.Fatal(err)
		}
	}
	checkpoint, _, err = s.GetEnrichmentCheckpoint(ctx)
	if err != nil || !checkpoint.CaptureComplete || checkpoint.Complete || !checkpoint.RetryInvalid {
		t.Fatalf("capture completion consumed unfinished legacy repair admission: %+v %v", checkpoint, err)
	}
}
