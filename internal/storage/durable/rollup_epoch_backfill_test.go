package durable

import (
	"context"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/power"
	"testing"
	"time"
)

type reviewFoundationPolicy struct{}

func (reviewFoundationPolicy) Effective(context.Context) (power.Effective, error) {
	e, _ := power.Resolve(power.Config{Mode: power.Performance}, power.SystemState{})
	return e, nil
}

type reviewFoundationModel struct{}

func (reviewFoundationModel) CountInputTokens(context.Context, chronicle.Packed) (int, error) {
	return 10, nil
}
func (reviewFoundationModel) Generate(context.Context, chronicle.Packed, int) (chronicle.Summary, error) {
	panic("hour generation not expected")
}
func (reviewFoundationModel) CountRollupTokens(context.Context, chronicle.RollupPrompt) (int, error) {
	return 10, nil
}
func (reviewFoundationModel) GenerateRollup(context.Context, chronicle.RollupPrompt, int) (string, error) {
	return "- Reviewed the release evidence.", nil
}
func TestReviewBackfillWorksAfterRollupErasureEpoch(t *testing.T) {
	ctx := context.Background()
	store, _, _ := rollupTestStore(t, 0xfd)
	start := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	source := rollupTestSource(t, store, "Reviewed release evidence.", start.Add(time.Minute))
	hour := chronicle.Summary{HourID: "hour:" + start.Format("20060102T15Z"), Start: start, End: start.Add(time.Hour), Text: "Reviewed release evidence.", SourceIDs: []string{source}}
	if err := store.PutChronicle(ctx, hour); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAllChronicleRollups(ctx); err != nil {
		t.Fatal(err)
	}
	worker := chronicle.NewWorker(store, reviewFoundationPolicy{}, reviewFoundationModel{}, chronicle.WorkerConfig{Enabled: true, Provider: power.LocalProvider, Now: func() time.Time { return time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC) }, Zone: func() string { return rollupTestZone }})
	defer worker.Close()
	if err := worker.ProcessRollups(ctx); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.GetChronicleRollup(ctx, "day:2026-11-15")
	if err != nil || !found || got.Text == "" {
		t.Fatalf("existing hour was stranded after rollup erasure: found=%v row=%+v err=%v", found, got, err)
	}
}
