package enrich_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/model"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

type invalidLibraryGenerator struct {
	digest, refuseID string
	refuseAfter      int
	failure          error
	calls            map[string]int
}

func (g *invalidLibraryGenerator) Identity(context.Context) (string, error) { return g.digest, nil }
func (*invalidLibraryGenerator) CountInputTokens(context.Context, []derive.Evidence) (int, error) {
	return 10, nil
}
func (g *invalidLibraryGenerator) Generate(_ context.Context, input []derive.Evidence) ([]derive.Proposal, error) {
	id := input[0].SourceID
	g.calls[id]++
	if (id == g.refuseID || g.refuseID == "*") && g.calls[id] > g.refuseAfter {
		return nil, g.failure
	}
	return []derive.Proposal{{Kind: knowledge.Project, Name: "Atlas", Fact: "Atlas has an offline export workflow.", SourceID: id}}, nil
}

func TestInvalidOutputRepairRetainsFourChunkQuantum(t *testing.T) {
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	for i := range 6 {
		source(t, store, fmt.Sprintf("Synthetic failed Atlas source %d.", i))
	}
	g := &invalidLibraryGenerator{digest: "bounded-invalid-repair", refuseID: "*", failure: model.ErrAppleResponse, calls: map[string]int{}}
	w, err := enrich.NewService(store, store, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	complete(t, w)
	w.RetryInvalidOutputs()
	if err := w.Backfill(context.Background()); !errors.Is(err, enrich.ErrMoreWork) {
		t.Fatal("manual repair bypassed the four-model-chunk quantum", err)
	}
	repeated := 0
	for _, calls := range g.calls {
		if calls == 2 {
			repeated++
		} else if calls != 1 {
			t.Fatal("a failed source was retried more than once in one pass")
		}
	}
	if repeated != 4 {
		t.Fatal("manual repair did not obey its admitted work budget", repeated)
	}
	complete(t, w)
	for _, calls := range g.calls {
		if calls != 2 {
			t.Fatal("resumed manual repair lost or repeated a source")
		}
	}
	w.Wake()
	complete(t, w)
	for _, calls := range g.calls {
		if calls != 2 {
			t.Fatal("routine wake retained manual retry authority")
		}
	}
}

func TestOutputDecoderUpgradeRepairsOnlyInvalidSourcesOnce(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := source(t, store, "Project Atlas has a local parser.")
	b := source(t, store, "Project Borealis has a local compiler.")
	g := &invalidLibraryGenerator{digest: "same-model-decoder-upgrade", refuseID: a.ContentID(), failure: model.ErrOllamaResponse, calls: map[string]int{}}
	w, err := enrich.NewService(store, store, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	complete(t, w)
	w.Close()
	// Preserve an actual old per-source decoder receipt as well as the old
	// directory checkpoint; current workers stamp their output revision.
	end, next, done, err := enrich.NextChunk(a.Text(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyEnrichment(ctx, enrich.Chunk{SourceID: a.ContentID(), ModelKey: enrich.ModelKey(g.digest), End: end, Next: next, Complete: done, Declined: true, FailureCode: enrich.FailureInvalidOutput, RetryInvalid: true}); err != nil {
		t.Fatal(err)
	}
	checkpoint, found, err := store.GetEnrichmentCheckpoint(ctx)
	if err != nil || !found {
		t.Fatal("checkpoint missing", err)
	}
	// Simulate a persisted checkpoint produced before the decoder repair.
	encoded, _ := json.Marshal(checkpoint)
	var fields map[string]any
	_ = json.Unmarshal(encoded, &fields)
	delete(fields, "output_revision")
	encoded, _ = json.Marshal(fields)
	checkpoint = enrich.Checkpoint{}
	_ = json.Unmarshal(encoded, &checkpoint)
	if err := store.PutEnrichmentCheckpoint(ctx, checkpoint); err != nil {
		t.Fatal(err)
	}
	g.refuseID = ""
	w, err = enrich.NewService(store, store, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	complete(t, w)
	progress, found, err := store.GetEnrichmentProgress(ctx, a.ContentID())
	if err != nil || !found || !progress.Complete || progress.Declined || g.calls[a.ContentID()] != 2 || g.calls[b.ContentID()] != 1 {
		t.Fatal("decoder upgrade did not repair just the invalid receipt", progress, g.calls, err)
	}
	w.Wake()
	complete(t, w)
	if g.calls[a.ContentID()] != 2 || g.calls[b.ContentID()] != 1 {
		t.Fatal("upgrade repair repeated unchanged work", g.calls)
	}
}

func TestDecoderUpgradeRetrySurvivesWakePendingAndReopenWithoutRepeating(t *testing.T) {
	ctx := context.Background()
	path, key := filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey()
	store, err := durable.OpenDurable(path, key, durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	g := &invalidLibraryGenerator{digest: "decoder-upgrade-still-invalid", refuseID: "*", failure: model.ErrOllamaResponse, calls: map[string]int{}}
	var invalidIDs []string
	for i := range 8 {
		o := source(t, store, fmt.Sprintf("Saved technical source number %d.", i))
		end, next, done, err := enrich.NextChunk(o.Text(), 0)
		if err != nil {
			t.Fatal(err)
		}
		chunk := enrich.Chunk{SourceID: o.ContentID(), ModelKey: enrich.ModelKey(g.digest), End: end, Next: next, Complete: done, Declined: i != 6}
		if i < 6 {
			chunk.FailureCode = enrich.FailureInvalidOutput
			invalidIDs = append(invalidIDs, o.ContentID())
		}
		// i==6 is completed work; i==7 is a native refusal. Neither may retry.
		if err := store.ApplyEnrichment(ctx, chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutEnrichmentCheckpoint(ctx, enrich.Checkpoint{ModelKey: enrich.ModelKey(g.digest), Complete: true, CaptureComplete: true, CoverageLimited: true}); err != nil {
		t.Fatal(err)
	}
	w, err := enrich.NewService(store, store, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	w.Wake()
	w.NotifySource(invalidIDs[0])
	if err := w.Backfill(ctx); !errors.Is(err, enrich.ErrMoreWork) {
		t.Fatal("upgrade lost bounded continuation", err)
	}
	if len(g.calls) != 4 {
		t.Fatal("upgrade did not process four distinct sources", g.calls)
	}
	for _, n := range g.calls {
		if n != 1 {
			t.Fatal("pending and scan repeated a receipt in one quantum", g.calls)
		}
	}
	w.Wake() // A wake during an admitted repair cannot renew its model authority.
	complete(t, w)
	if len(g.calls) != 6 {
		t.Fatal("upgrade retried completed or native-refused work", g.calls)
	}
	for _, n := range g.calls {
		if n != 1 {
			t.Fatal("wake repeated the same failed decoder attempt", g.calls)
		}
	}
	w.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = durable.OpenDurable(path, key, durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	w, err = enrich.NewService(store, store, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.Wake()
	complete(t, w)
	if len(g.calls) != 6 {
		t.Fatal("reopen expanded retry authority", g.calls)
	}
	for _, n := range g.calls {
		if n != 1 {
			t.Fatal("encrypted reopen lost per-source retry history", g.calls)
		}
	}
}

func TestInvalidLibraryOutputDoesNotBlockLaterSourcesOrRepeatAfterReopen(t *testing.T) {
	ctx := context.Background()
	path, key := filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey()
	store, err := durable.OpenDurable(path, key, durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	observations := []capture.SanitizedObservation{source(t, store, "Synthetic Atlas source A."), source(t, store, "Synthetic Atlas source B.")}
	sort.Slice(observations, func(i, j int) bool { return observations[i].ContentID() < observations[j].ContentID() })
	first, second := observations[0].ContentID(), observations[1].ContentID()
	g := &invalidLibraryGenerator{digest: "invalid-output-fixture", refuseID: first, failure: model.ErrAppleResponse, calls: map[string]int{}}
	w, err := enrich.NewService(store, store, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.NotifySource(first)
	w.NotifySource(second)
	complete(t, w)
	progress, found, err := store.GetEnrichmentProgress(ctx, first)
	encoded, _ := json.Marshal(progress)
	var fields map[string]any
	_ = json.Unmarshal(encoded, &fields)
	if err != nil || !found || !progress.Declined || progress.Complete || progress.Next != 0 || fields["failure_code"] != "invalid_output" || len(progress.Entities) != 0 {
		t.Fatal("invalid output must retain an explicit incomplete receipt without generated claims", err)
	}
	if g.calls[first] != 1 || g.calls[second] != 1 || !w.Status().CoverageLimited || w.Status().LastErrorCode != "invalid_output" || w.Status().ProcessedChunks != 1 {
		t.Fatal("one invalid result blocked later work or was counted as success")
	}
	w.Wake()
	complete(t, w)
	if g.calls[first] != 1 {
		t.Fatal("routine repair repeated an unchanged invalid source")
	}
	w.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = durable.OpenDurable(path, key, durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	w, err = enrich.NewService(store, store, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	complete(t, w)
	if g.calls[first] != 1 || !w.Status().CoverageLimited {
		t.Fatal("reopen lost the failed source receipt or retried it automatically")
	}
}

func TestManualInvalidOutputRepairPreservesPrefixAndClearsCurrentIssue(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	o := source(t, store, strings.Repeat("Atlas is an offline export tool. ", 80))
	g := &invalidLibraryGenerator{digest: "invalid-tail-fixture", refuseID: o.ContentID(), refuseAfter: 1, failure: model.ErrOllamaResponse, calls: map[string]int{}}
	w, err := enrich.NewService(store, store, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	complete(t, w)
	prefix, found, err := store.GetEnrichmentProgress(ctx, o.ContentID())
	if err != nil || !found || prefix.Next != enrich.ChunkBytes-enrich.ChunkOverlap || prefix.Complete || !prefix.Declined || len(prefix.Entities) != 1 {
		t.Fatal("invalid tail changed its valid prefix", err)
	}
	g.refuseID = ""
	repair, ok := any(w).(interface{ RetryInvalidOutputs() })
	if !ok {
		t.Fatal("explicit bounded invalid-output repair is unavailable")
	}
	repair.RetryInvalidOutputs()
	complete(t, w)
	progress, found, err := store.GetEnrichmentProgress(ctx, o.ContentID())
	if err != nil || !found || !progress.Complete || progress.Declined || len(progress.Entities) != 1 || w.Status().CoverageLimited || w.Status().ErrorCode != "" || w.Status().LastErrorCode != "invalid_output" {
		t.Fatal("manual recovery lost prefix, retained a current issue, or erased error history", err)
	}
}
