package chronicle

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

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

type BackgroundPolicy interface {
	Effective(context.Context) (power.Effective, error)
}

type WorkerConfig struct {
	Enabled  bool
	Provider power.Provider
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
}

func NewWorker(repository SummaryRepository, policy BackgroundPolicy, generator SummaryGenerator, config WorkerConfig) *Worker {
	ctx, cancel := context.WithCancel(context.Background())
	w := &Worker{repository: repository, policy: policy, generator: generator, config: config, queue: make(chan workerJob, MaxQueuedChronicleHours), ctx: ctx, cancel: cancel, done: make(chan struct{}), queued: make(map[string]struct{}), activeChanged: make(chan struct{}), logger: slog.Default()}
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
			if _, found, err := w.repository.GetChronicle(w.ctx, job.packed.HourID); err == nil && found {
				w.clearPending(job.packed.HourID)
				continue
			} else if err != nil {
				w.logger.Warn("chronicle job skipped", "result", "existing_summary_check_failed")
				w.clearPending(job.packed.HourID)
				continue
			}
			if err := w.process(w.ctx, job.packed); err != nil && !errors.Is(err, context.Canceled) {
				w.logJobError(err)
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
	w.logger.Warn("chronicle job failed", "result", "failed")
}

func isProviderUnavailable(err error) bool {
	return errors.Is(err, model.ErrOllamaUnavailable) || errors.Is(err, model.ErrCompatibleUnavailable)
}

func (w *Worker) process(parent context.Context, packed Packed) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if w == nil || !w.enabled() || !validPacked(packed) {
		return ErrWorkerUnavailable
	}
	started := time.Now()
	tokens, err := w.generator.CountInputTokens(parent, packed)
	if err != nil {
		return err
	}
	if tokens < 1 || tokens > MaxEvidenceBytes {
		return ErrWorkerPolicy
	}
	effective, err := w.policy.Effective(parent)
	if err != nil && !errors.Is(err, power.ErrSystemStateUnavailable) {
		return err
	}
	// Unknown OS power is explicitly resolved by the policy controller to
	// Balanced; any other policy/configuration error fails closed above.
	if !effective.AllowBackgroundAI(power.LocalProvider, tokens+MaxChronicleOutputTokens, effective.MaxBackgroundDuration) {
		return ErrWorkerPolicy
	}
	budget := effective.MaxBackgroundDuration - time.Since(started)
	if budget <= 0 || budget > power.MaxBackgroundDuration {
		return ErrWorkerPolicy
	}
	ctx, cancel := context.WithTimeout(parent, budget)
	stop := context.AfterFunc(w.ctx, cancel)
	defer func() { stop(); cancel() }()
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
	if err := ValidateSummary(packed, summary); err != nil {
		return err
	}
	if err := w.repository.PutChronicle(ctx, summary); err != nil {
		return err
	}
	w.logger.Info("chronicle summary stored", "result", "stored", "source_count", len(summary.SourceIDs))
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
