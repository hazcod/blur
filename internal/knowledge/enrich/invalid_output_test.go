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
