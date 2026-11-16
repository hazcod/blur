package chronicle

import (
	"context"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/power"
	"strings"
	"testing"
	"time"
)

type subsetReceiptModel struct{ workerModel }

func (m *subsetReceiptModel) Generate(ctx context.Context, p Packed, max int) (Summary, error) {
	s, err := m.workerModel.Generate(ctx, p, max)
	if err == nil {
		s.SourceIDs = s.SourceIDs[:1]
	}
	return s, err
}

func waitReceiptWorker(t *testing.T, w *Worker, repo *workerRepo, h Hour) {
	t.Helper()
	packed, err := PackHour(h)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		row, found, err := repo.GetChronicle(context.Background(), packed.HourID)
		w.mu.Lock()
		pending := len(w.queued)
		w.mu.Unlock()
		if err == nil && found && row.InputReceipt == inputReceipt(packed) && pending == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("changed input did not finish and record its exact receipt")
}

func TestRecapRefreshesNewInputAndSkipsAlreadyProcessedCitationSubset(t *testing.T) {
	ctx := context.Background()
	repo := &workerRepo{rows: map[string]Summary{}}
	model := &subsetReceiptModel{workerModel: workerModel{count: 10}}
	worker := NewWorker(repo, &workerPolicy{value: performance()}, model, WorkerConfig{Enabled: true, Provider: power.LocalProvider})
	defer worker.Close()
	hour := workerHour(t)
	add := func(label string) {
		o, err := capture.SanitizeObservation("editor.app", "", strings.Repeat(label+" ", 10), 2048)
		if err != nil {
			t.Fatal(err)
		}
		hour.Evidence = append(hour.Evidence, Evidence{Observation: o, At: hour.Start.Add(time.Duration(len(hour.Evidence)+1) * time.Minute), Salience: 5})
	}
	add("compiler")
	if err := worker.Schedule(ctx, hour); err != nil {
		t.Fatal(err)
	}
	waitReceiptWorker(t, worker, repo, hour)
	add("rollback")
	if err := worker.Schedule(ctx, hour); err != nil {
		t.Fatal(err)
	}
	waitReceiptWorker(t, worker, repo, hour)
	if model.callCount() != 2 {
		t.Fatal("newly indexed evidence failed to refresh recap", model.callCount())
	}
	if err := worker.Schedule(ctx, hour); err != nil {
		t.Fatal(err)
	}
	waitReceiptWorker(t, worker, repo, hour)
	if model.callCount() != 2 {
		t.Fatal("model citation subset caused repeated automatic inference", model.callCount())
	}
}
