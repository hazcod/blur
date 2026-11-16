package enrich

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/model"
)

func TestLibraryStatusSnapshotsKnownQueueWithoutCallingPorts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stamp := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	service := &Service{ctx: ctx, runCancel: cancel, pending: map[string]bool{"private-source-id": true}, status: Status{State: "indexing", ProcessedChunks: 3, LastCompletedAt: stamp, DeferredReason: "foreground"}}
	for range 100 {
		status := service.Status()
		if !status.Active || status.Queued != 1 || status.ProcessedChunks != 3 || status.DeferredReason != "foreground" || !status.LastCompletedAt.Equal(stamp) {
			t.Fatal("library snapshot lost actual bookkeeping", status)
		}
	}
	status := service.Status()
	status.Queued = 999
	if service.Status().Queued != 1 || len(service.pending) != 1 {
		t.Fatal("status mutated source queue")
	}
	// Requesting cancellation does not retire an actual provider call. Keep
	// the run visible until Backfill's defer clears its ownership marker.
	service.closed = true
	if !service.Status().Active {
		t.Fatal("close request falsely reported an unretired backfill as idle")
	}
	service.runCancel = nil
	if service.Status().Active {
		t.Fatal("retired backfill still reported active")
	}
}

func TestLibraryStatusClassifiesAppleFailuresWithoutRawDetails(t *testing.T) {
	for _, test := range []struct {
		err  error
		code string
	}{
		{model.ErrAppleUnavailable, "provider_unavailable"},
		{model.ErrAppleBudget, "model_budget_exceeded"},
		{model.ErrAppleResponse, "invalid_output"},
		{model.ErrAppleRefused, "model_refused"},
		{model.ErrAppleConfig, "model_configuration"},
		{errors.New("private prompt or backend detail"), "local_processing_failed"},
	} {
		if got := errorCode(test.err); got != test.code {
			t.Fatalf("closed classification=%s, want %s", got, test.code)
		}
	}
}
