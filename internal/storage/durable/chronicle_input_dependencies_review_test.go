package durable

import (
	"context"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/power"
)

type subsetDependencyGenerator struct{}

func (subsetDependencyGenerator) CountInputTokens(context.Context, chronicle.Packed) (int, error) {
	return 32, nil
}
func (subsetDependencyGenerator) Generate(_ context.Context, input chronicle.Packed, _ int) (chronicle.Summary, error) {
	return chronicle.Summary{HourID: input.HourID, Start: input.Start, End: input.End, Text: "Discussed synthetic Alpha and Beta.", SourceIDs: []string{input.SourceIDs[0]}}, nil
}

func TestChronicleDependenciesEraseUncitedGeneratedInputs(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xd8)
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	alpha, err := capture.SanitizeObservation("editor.app", "", "Synthetic Alpha discussed", 1024)
	if err != nil {
		t.Fatal(err)
	}
	beta, err := capture.SanitizeObservation("editor.app", "", "Synthetic Beta discussed", 1024)
	if err != nil {
		t.Fatal(err)
	}
	for _, observation := range []capture.SanitizedObservation{alpha, beta} {
		if err := store.PutObservationAt(ctx, observation, start.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	hour, err := store.CaptureHourEvidence(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	worker := chronicle.NewWorker(store, deletingChroniclePolicy{}, subsetDependencyGenerator{}, chronicle.WorkerConfig{Enabled: true, Provider: power.LocalProvider})
	if err := worker.Process(ctx, hour); err != nil {
		worker.Close()
		t.Fatal(err)
	}
	worker.Close()
	row, found, err := store.GetChronicle(ctx, "hour:"+start.Format("20060102T15Z"))
	if err != nil || !found || len(row.SourceIDs) != 1 || row.InputReceipt == "" {
		t.Fatal("synthetic worker did not accept a valid citation subset", found, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	uncited := alpha.ContentID()
	if row.SourceIDs[0] == uncited {
		uncited = beta.ContentID()
	}
	if err := store.DeleteObservationRecord(ctx, uncited); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetChronicle(ctx, row.HourID); err != nil || found {
		t.Fatal("recap derived from an erased uncited input survived", found, err)
	}
	if _, found, err := store.GetObservation(ctx, row.SourceIDs[0]); err != nil || !found {
		t.Fatal("erasing an uncited dependency also erased the retained cited primary", found, err)
	}
	if err := store.PutChronicle(ctx, row); err == nil {
		t.Fatal("late recap replay resurrected an erased uncited dependency")
	}
}
