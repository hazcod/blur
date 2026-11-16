package chronicle

import (
	"context"
	"errors"
	"time"

	"github.com/hazcod/blur/internal/model"
)

type WorkerStatus struct {
	State, Reason              string
	Queued, Running, Completed int
	LastCompletedAt            time.Time
	LastErrorCode              string
	LastErrorAt                time.Time
	// RollupsPending counts dirty day and week rollups that can be generated
	// now; RollupsDirty counts every dirty one. RollupReason is non-empty when
	// rollup generation is skipped, for example skipped_no_local_provider.
	RollupsPending, RollupsDirty int
	RollupReason                 string
}
type workerTelemetry struct {
	deferred        int
	completed       int
	reason          string
	lastCompletedAt time.Time
	lastErrorCode   string
	lastErrorAt     time.Time
	rollupsPending  int
	rollupsDirty    int
	rollupReason    string
}

// Status snapshots existing queue/admission counters only. It never reads
// sources, power state, model availability, repository or native capabilities.
func (w *Worker) Status() WorkerStatus {
	if w == nil {
		return WorkerStatus{State: "disabled"}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	out := WorkerStatus{State: "idle", Queued: max(0, len(w.queued)-w.activeJobs), Running: w.activeJobs, Completed: w.telemetry.completed, LastCompletedAt: w.telemetry.lastCompletedAt, LastErrorCode: w.telemetry.lastErrorCode, LastErrorAt: w.telemetry.lastErrorAt, RollupsPending: w.telemetry.rollupsPending, RollupsDirty: w.telemetry.rollupsDirty, RollupReason: w.telemetry.rollupReason}
	switch {
	case w.closed || !w.enabled():
		out.State = "disabled"
	case w.telemetry.deferred > 0:
		out.State = "deferred"
		out.Reason = "foreground"
	case out.Running > 0:
		out.State = "running"
	case out.Queued > 0:
		out.State = "queued"
	case w.telemetry.reason != "" && w.telemetry.reason != "foreground":
		out.State = "unavailable"
		out.Reason = w.telemetry.reason
	}
	return out
}
func (w *Worker) recordAttempt(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err == nil {
		w.telemetry.completed++
		w.telemetry.lastCompletedAt = time.Now().UTC()
		w.telemetry.reason = ""
		return
	}
	if errors.Is(err, model.ErrBackgroundPreempted) {
		w.telemetry.reason = "foreground"
		return
	}
	if errors.Is(err, context.Canceled) || w.ctx.Err() != nil {
		return
	}
	code := workerErrorCode(err)
	w.telemetry.reason = code
	w.telemetry.lastErrorCode = code
	w.telemetry.lastErrorAt = time.Now().UTC()
}

func workerErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrWorkerPolicy):
		return "policy"
	case isProviderUnavailable(err):
		return "provider_unavailable"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrWorkerUnavailable):
		return "source_unavailable"
	case errors.Is(err, model.ErrAppleBudget), errors.Is(err, model.ErrOllamaBudget):
		return "model_budget_exceeded"
	case errors.Is(err, model.ErrAppleResponse), errors.Is(err, model.ErrOllamaResponse), errors.Is(err, ErrInvalid):
		return "invalid_output"
	case errors.Is(err, model.ErrAppleRefused):
		return "model_refused"
	case errors.Is(err, model.ErrAppleConfig):
		return "model_configuration"
	default:
		return "processing_failed"
	}
}
