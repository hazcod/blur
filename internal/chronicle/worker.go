package chronicle

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/model"
	"github.com/hazcod/blur/internal/power"
)

const MaxChronicleOutputTokens = 384
const MaxQueuedChronicleHours = 24

var (
	ErrWorkerDisabled    = errors.New("chronicle: background summaries are disabled or not local")
	ErrWorkerDuplicate   = errors.New("chronicle: hour is already queued or running")
	ErrWorkerBusy        = errors.New("chronicle: bounded worker queue is full")
	ErrWorkerPolicy      = errors.New("chronicle: background policy denied the job")
	ErrWorkerUnavailable = errors.New("chronicle: worker unavailable")
)

type SummaryRepository interface {
	PutChronicle(context.Context, Summary) error
	GetChronicle(context.Context, string) (Summary, bool, error)
}

type SummaryGenerator interface {
	CountInputTokens(context.Context, Packed) (int, error)
	Generate(context.Context, Packed, int) (Summary, error)
}

// summaryByteAdmission checks serialized text bounds before the provider's
// tokenizer, whose own format validation may reject an oversized request.
type summaryByteAdmission interface {
	FitsRequestBytes(Packed) (bool, error)
}

type BackgroundPolicy interface {
	Effective(context.Context) (power.Effective, error)
}

type WorkerConfig struct {
	Enabled  bool
	Provider power.Provider
	// Now and Zone drive day and week rollups. Zone returns the IANA name of
	// the zone whose local days and ISO weeks are summarized; both default to
	// the process clock and zone.
	Now  func() time.Time
	Zone func() string
}

type workerJob struct{ packed Packed }

// Worker performs optional closed-hour work on a bounded worker pool. It owns
// no capture source and never polls: callers explicitly submit completed,
// sanitized Hour values. Consent and route are immutable for this instance.
type Worker struct {
	repository    SummaryRepository
	policy        BackgroundPolicy
	generator     SummaryGenerator
	config        WorkerConfig
	queue         chan workerJob
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{}
	mu            sync.Mutex
	closed        bool
	queued        map[string]struct{}
	activeJobs    int
	activeChanged chan struct{}
	logger        *slog.Logger
	deferredLog   sync.Once
	telemetry     workerTelemetry
	// rollupMu serializes rollup passes; rollupBackfilled is guarded by it.
	rollupMu         sync.Mutex
	rollupBackfilled bool
	rollupKick       chan struct{}
}

func NewWorker(repository SummaryRepository, policy BackgroundPolicy, generator SummaryGenerator, config WorkerConfig) *Worker {
	ctx, cancel := context.WithCancel(context.Background())
	w := &Worker{repository: repository, policy: policy, generator: generator, config: config, queue: make(chan workerJob, MaxQueuedChronicleHours), ctx: ctx, cancel: cancel, done: make(chan struct{}), queued: make(map[string]struct{}), activeChanged: make(chan struct{}), logger: slog.Default(), rollupKick: make(chan struct{}, 1)}
	go w.run()
	return w
}

func (w *Worker) Schedule(ctx context.Context, hour Hour) error {
	if w == nil || ctx == nil || ctx.Err() != nil || !w.enabled() {
		return ErrWorkerDisabled
	}
	packed, err := PackHour(hour)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return ErrWorkerUnavailable
	}
	if _, ok := w.queued[packed.HourID]; ok {
		return ErrWorkerDuplicate
	}
	w.queued[packed.HourID] = struct{}{}
	select {
	case w.queue <- workerJob{packed: packed}:
		return nil
	default:
		delete(w.queued, packed.HourID)
		return ErrWorkerBusy
	}
}

// Process runs one explicitly user- or scheduler-submitted closed period and
// is primarily useful to bounded embedding services and deterministic tests.
func (w *Worker) Process(ctx context.Context, hour Hour) error {
	if w == nil || ctx == nil || ctx.Err() != nil || !w.enabled() {
		return ErrWorkerDisabled
	}
	packed, err := PackHour(hour)
	if err != nil {
		return err
	}
	return w.process(ctx, packed)
}

func (w *Worker) Close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		w.cancel()
	}
	w.mu.Unlock()
	<-w.done
	if closer, ok := w.generator.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (w *Worker) enabled() bool {
	return w != nil && w.config.Enabled && w.config.Provider == power.LocalProvider && w.repository != nil && w.policy != nil && w.generator != nil
}

func (w *Worker) run() {
	defer close(w.done)
	var workers sync.WaitGroup
	for i := 0; i < power.MaxBackgroundAIJobs; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); w.consume() }()
	}
	workers.Add(1)
	go func() { defer workers.Done(); w.rollupLoop() }()
	workers.Wait()
}

func (w *Worker) consume() {
	for {
		select {
		case <-w.ctx.Done():
			return
		case job := <-w.queue:
			w.mu.Lock()
			closed := w.closed
			w.mu.Unlock()
			if closed {
				return
			}
			if existing, found, err := w.repository.GetChronicle(w.ctx, job.packed.HourID); err == nil && found && existing.InputReceipt == inputReceipt(job.packed) {
				w.clearPending(job.packed.HourID)
				continue
			} else if err != nil {
				w.logger.Warn("chronicle job skipped", "result", "existing_summary_check_failed")
				w.recordAttempt(err)
				w.clearPending(job.packed.HourID)
				continue
			}
			receipts := make(map[string]time.Time, len(job.packed.SourceIDs))
			for {
				err := w.processAttempt(model.WithBackgroundInferenceGroup(w.ctx, "recaps"), job.packed, receipts)
				_, canRevalidate := w.repository.(summarySourceReader)
				if errors.Is(err, model.ErrBackgroundPreempted) && !canRevalidate {
					err = ErrWorkerUnavailable
					w.recordAttempt(err)
				}
				if errors.Is(err, model.ErrBackgroundPreempted) && w.ctx.Err() == nil {
					// Retain this bounded active job and its queued membership. A
					// new attempt rechecks power, source receipts and cancellation.
					w.mu.Lock()
					w.telemetry.deferred++
					w.mu.Unlock()
					timer := time.NewTimer(250 * time.Millisecond)
					select {
					case <-w.ctx.Done():
					case <-timer.C:
					}
					timer.Stop()
					w.mu.Lock()
					w.telemetry.deferred--
					w.mu.Unlock()
					if w.ctx.Err() == nil {
						continue
					}
				}
				if err != nil && w.ctx.Err() == nil && !errors.Is(err, context.Canceled) {
					w.logJobError(err)
				}
				break
			}
			w.clearPending(job.packed.HourID)
		}
	}
}

// logJobError classifies a failed job. A provider that is simply not running
// (Ollama closed) is an expected state: it is logged once per worker at INFO as
// a deferral, because every queued hour would hit the same cause. Anything else
// is a real failure and is logged at WARN per job.
func (w *Worker) logJobError(err error) {
	if isProviderUnavailable(err) {
		w.deferredLog.Do(func() {
			w.logger.Info("chronicle job deferred", "result", "provider_unavailable")
		})
		return
	}
	w.logger.Warn("chronicle job failed", "result", "failed", "code", workerErrorCode(err))
}

func isProviderUnavailable(err error) bool {
	return errors.Is(err, model.ErrOllamaUnavailable) || errors.Is(err, model.ErrAppleUnavailable) || errors.Is(err, model.ErrCompatibleUnavailable)
}

func (w *Worker) process(parent context.Context, packed Packed) error {
	return w.processAttempt(parent, packed, nil)
}

func (w *Worker) processAttempt(parent context.Context, packed Packed, receipts map[string]time.Time) (result error) {
	if w != nil {
		w.mu.Lock()
		w.telemetry.reason = ""
		w.mu.Unlock()
		defer func() { w.recordAttempt(result) }()
	}
	if err := parent.Err(); err != nil {
		return err
	}
	if w == nil || !w.enabled() || !validPacked(packed) {
		return ErrWorkerUnavailable
	}
	input := inputReceipt(packed)
	if receipts == nil {
		receipts = make(map[string]time.Time, len(packed.SourceIDs))
	}
	if err := w.verifySources(parent, packed, receipts); err != nil {
		return err
	}
	started := time.Now()
	effective, err := w.policy.Effective(parent)
	if err != nil && !errors.Is(err, power.ErrSystemStateUnavailable) {
		return err
	}
	// Unknown OS power is explicitly resolved by the policy controller to
	// Balanced; any other policy/configuration error fails closed above.
	if effective.MaxBackgroundAIJobs < 1 || effective.MaxBackgroundTokens <= MaxChronicleOutputTokens {
		return ErrWorkerPolicy
	}
	budget := effective.MaxBackgroundDuration - time.Since(started)
	if budget <= 0 || budget > power.MaxBackgroundDuration {
		return ErrWorkerPolicy
	}
	ctx, cancel := context.WithTimeout(parent, budget)
	stop := context.AfterFunc(w.ctx, cancel)
	defer func() { stop(); cancel() }()
	capacity, err := model.RuntimeContextLimit(ctx, w.generator, model.ContextTokenLimit)
	if err != nil {
		return err
	}
	inputLimit := min(MaxEvidenceBytes, effective.MaxBackgroundTokens-MaxChronicleOutputTokens, capacity-MaxChronicleOutputTokens-model.PromptTemplateReserve)
	if inputLimit < 1 {
		return ErrWorkerPolicy
	}
	for attempt := 0; ; attempt++ {
		fitsBytes := true
		if admission, ok := w.generator.(summaryByteAdmission); ok {
			fitsBytes, err = admission.FitsRequestBytes(packed)
			if err != nil {
				return err
			}
		}
		if fitsBytes {
			tokens, err := w.generator.CountInputTokens(ctx, packed)
			if err != nil {
				return err
			}
			if tokens < 1 {
				return ErrWorkerPolicy
			}
			if tokens <= inputLimit && effective.AllowBackgroundAI(power.LocalProvider, tokens+MaxChronicleOutputTokens, effective.MaxBackgroundDuration) {
				break
			}
		}
		if attempt+1 >= maxAdmissionCounts {
			return ErrWorkerPolicy
		}
		var smaller bool
		packed, smaller = shrinkPacked(packed)
		if !smaller {
			return ErrWorkerPolicy
		}
	}
	release, err := w.acquire(ctx, effective.MaxBackgroundAIJobs)
	if err != nil {
		return err
	}
	defer release()
	summary, err := w.generator.Generate(ctx, packed, MaxChronicleOutputTokens)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	summary, err = applyCoverage(packed, summary)
	if err != nil {
		return err
	}
	if err := ValidateSummary(packed, summary); err != nil {
		return err
	}
	if err := w.verifySources(ctx, packed, receipts); err != nil {
		return err
	}
	summary.InputReceipt = input
	summary.InputSourceIDs = append([]string(nil), packed.SourceIDs...)
	if err := w.repository.PutChronicle(ctx, summary); err != nil {
		return err
	}
	w.logger.Info("chronicle summary stored", "result", "stored", "source_count", len(summary.SourceIDs))
	w.kickRollups()
	return nil
}

func (w *Worker) acquire(ctx context.Context, limit int) (func(), error) {
	if limit < 1 || limit > power.MaxBackgroundAIJobs {
		return nil, ErrWorkerPolicy
	}
	for {
		w.mu.Lock()
		if w.activeJobs < limit {
			w.activeJobs++
			w.mu.Unlock()
			return func() {
				w.mu.Lock()
				w.activeJobs--
				close(w.activeChanged)
				w.activeChanged = make(chan struct{})
				w.mu.Unlock()
			}, nil
		}
		changed := w.activeChanged
		w.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (w *Worker) clearPending(id string) { w.mu.Lock(); delete(w.queued, id); w.mu.Unlock() }

// Production repositories expose immutable sources. Retain saved-first clocks
// separately from evidence visit clocks, so revisits remain valid while a
// delete/recreate under the same content ID cannot resume an old packed job.
type summarySourceReader interface {
	GetObservationWithCaptureTime(context.Context, string) (capture.SanitizedObservation, time.Time, bool, error)
}

type summaryEvidenceReader interface {
	GetSourceEvidenceWithCaptureTime(context.Context, string) (capture.Evidence, time.Time, bool, error)
}

func (w *Worker) verifySources(ctx context.Context, packed Packed, receipts map[string]time.Time) error {
	reader, ok := w.repository.(summarySourceReader)
	evidenceReader, generic := w.repository.(summaryEvidenceReader)
	if !ok && !generic {
		return nil
	}
	for _, id := range packed.SourceIDs {
		var source capture.Evidence
		var at time.Time
		var found bool
		var err error
		if generic {
			source, at, found, err = evidenceReader.GetSourceEvidenceWithCaptureTime(ctx, id)
		} else {
			var observation capture.SanitizedObservation
			observation, at, found, err = reader.GetObservationWithCaptureTime(ctx, id)
			if err == nil && found {
				source, err = capture.ObservationEvidence(observation)
			}
		}
		if err != nil {
			return err
		}
		if !found || source.Validate() != nil || source.SourceID() != id || at.IsZero() {
			return ErrWorkerUnavailable
		}
		if original, recorded := receipts[id]; recorded && !original.Equal(at) {
			return ErrWorkerUnavailable
		}
		if receipts != nil {
			receipts[id] = at
		}
	}
	return ctx.Err()
}
