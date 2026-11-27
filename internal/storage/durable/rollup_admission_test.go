package durable

import (
	"context"
	"fmt"
	"github.com/hazcod/blur/internal/chronicle"
	"slices"
	"sort"
	"testing"
	"time"
)

func TestCalendarPeriodsUseDedicatedShards(t *testing.T) {
	s, _, _ := rollupTestStore(t, 0xec)
	s.SetChronicleZone(func() string { return "UTC" })
	for _, id := range []string{"month:2026-11", "year:2026"} {
		row, err := chronicle.NewRollupPlaceholder(id, "UTC")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutChronicleRollup(context.Background(), row); err != nil {
			t.Fatal(err)
		}
		got, found, err := s.GetChronicleRollup(context.Background(), id)
		if err != nil || !found || got.ID != id {
			t.Fatalf("calendar period did not reopen through its own index: %s %+v %v %v", id, got, found, err)
		}
	}
}

func TestRollupGeneratedSourceUnionFitsTransactionBudget(t *testing.T) {
	ctx := context.Background()
	store, _, _ := rollupTestStore(t, 0xee)
	var all, children []string
	var inputs []chronicle.RollupChild
	for h := 9; h < 13; h++ {
		at := time.Date(2026, 11, 15, h, 10, 0, 0, time.UTC)
		var batch []string
		for n := 0; n < 64; n++ {
			id := rollupTestSource(t, store, fmt.Sprintf("Reviewed unique document hour%d item%d", h, n), at)
			batch = append(batch, id)
			all = append(all, id)
		}
		sort.Strings(batch)
		start := at.Truncate(time.Hour)
		hour := chronicle.Summary{HourID: "hour:" + start.Format("20060102T15Z"), Start: start, End: start.Add(time.Hour), Text: "Reviewed the release documents.", SourceIDs: batch}
		if err := store.PutChronicle(ctx, hour); err != nil {
			t.Fatalf("admit hour: %v", err)
		}
		children = append(children, hour.HourID)
		inputs = append(inputs, chronicle.RollupChild{ID: hour.HourID, Label: "Observed hour", Text: hour.Text, SourceIDs: batch})
	}
	sort.Strings(all)
	input := chronicle.NewRollupInput("day", inputs)
	if len(input.SourceIDs()) > 64 || !input.Omitted {
		t.Fatalf("generated provenance does not fit a bounded transaction: %d sources sampled=%v", len(input.SourceIDs()), input.Omitted)
	}
	all = input.SourceIDs()
	prior, found, err := store.GetChronicleRollup(ctx, "day:2026-11-15")
	if err != nil || !found {
		t.Fatalf("placeholder: %v %v", found, err)
	}
	day := rollupTestRecord(t, "day:2026-11-15", "- Reviewed the release documents.", children, all)
	day.Revision = prior.Revision
	if err := store.PutChronicleRollup(ctx, day); err != nil {
		t.Fatalf("valid %d-source rollup exceeded public-write budget: %v", len(all), err)
	}
}

func TestRollupStalePutAfterDeleteAll(t *testing.T) {
	ctx := context.Background()
	store, path, _ := rollupTestStore(t, 0xef)
	start := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	source := rollupTestSource(t, store, "Reviewed release source retained after recap erasure", start.Add(time.Minute))
	hour := chronicle.Summary{HourID: "hour:" + start.Format("20060102T15Z"), Start: start, End: start.Add(time.Hour), Text: "Reviewed release source.", SourceIDs: []string{source}}
	if err := store.PutChronicle(ctx, hour); err != nil {
		t.Fatal(err)
	}
	prior, found, err := store.GetChronicleRollup(ctx, "day:2026-11-15")
	if err != nil || !found {
		t.Fatalf("placeholder %v %v", found, err)
	}
	generated := rollupTestRecord(t, prior.ID, "- Reviewed release source.", []string{hour.HourID}, []string{source})
	generated.Revision = prior.Revision
	if err := store.PutChronicleRollup(ctx, generated); err != nil {
		t.Fatal(err)
	}
	// Simulate an already-computed stale generation arriving after erasure.
	if err := store.DeleteAllChronicles(ctx); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetChronicleRollup(ctx, prior.ID); err != nil || found {
		t.Fatalf("erasure did not finish: %v %v", found, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenDurable(path, durableTestKey(t, 0xef), OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// A recreated Revision0 period still belongs to a different generation.
	if err := store.MarkChronicleRollupDirty(ctx, prior.ID, prior.Zone); err != nil {
		t.Fatal(err)
	}
	if err := store.PutChronicleRollup(ctx, generated); err != nil {
		t.Fatal(err)
	}
	if got, found, err := store.GetChronicleRollup(ctx, prior.ID); err != nil || !found || got.Text != "" || got.Epoch == generated.Epoch {
		t.Fatalf("stale generation resurrected erased recap: found=%v text=%q error=%v", found, got.Text, err)
	}
}

func TestLegacyRollupDisjointSourceReplacementDrainsWithinWorkerBudgets(t *testing.T) {
	ctx := context.Background()
	s, _, _ := rollupTestStore(t, 0xdd)
	at := time.Date(2018, 2, 6, 9, 0, 0, 0, time.UTC)
	var oldSources, newSources []string
	for n := 0; n < 320; n++ {
		id := rollupTestSource(t, s, fmt.Sprintf("Disjoint historical receipt source%d", n), at.Add(time.Minute))
		if n < 256 {
			oldSources = append(oldSources, id)
		} else {
			newSources = append(newSources, id)
		}
	}
	sort.Strings(oldSources)
	sort.Strings(newSources)
	legacy := rollupTestRecord(t, "day:2018-02-06", "- Legacy historical decision.", []string{"hour:20180206T09Z"}, oldSources)
	// Install an authenticated historical record through the private old-format
	// writer, without asking the modern public writer to admit256new sources.
	installLegacyRollupForTest(t, s, legacy)
	hour := chronicle.Summary{HourID: "hour:20180206T09Z", Start: at, End: at.Add(time.Hour), Text: "Current replacement decision.", SourceIDs: newSources}
	if err := s.PutChronicle(ctx, hour); err != nil {
		t.Fatal(err)
	}
	w := historicalRollupWorker(s)
	defer w.Close()
	for pass := 0; pass < 12; pass++ {
		if err := w.ProcessRollups(ctx); err != nil {
			t.Fatalf("legacy replacement work exceeded bounded transaction: %v", err)
		}
	}
	got, found, err := s.GetChronicleRollup(ctx, legacy.ID)
	if err != nil || !found || got.Dirty || got.Text == "" || !slices.Equal(got.SourceIDs, newSources) {
		t.Fatalf("legacy transition stranded: found%v row%+v err%v", found, got, err)
	}
	if err := s.Update(ctx, func(tx *Tx) error {
		for _, id := range oldSources {
			ids, found, err := tx.readRollupSources(id)
			if err != nil || found || len(ids) != 0 {
				t.Fatalf("old reverse receipt survived: %v %v %v", ids, found, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
