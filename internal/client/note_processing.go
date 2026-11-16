package client

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/hazcod/blur/internal/model"

	"github.com/hazcod/blur/internal/notes"
)

const maxNoteProcessingJobs = 8 // Includes the one actual in-flight extractor.
const maxCompletedNoteJobs = 256

type NoteProcessingStatus struct {
	State     string
	ErrorCode string
	Pending   int
}

type noteProcessingKey struct {
	sourceID                 string
	memoryEpoch, policyEpoch uint64
}
type noteProcessingJob struct {
	key     noteProcessingKey
	service *notes.Service
}
type noteProcessingWorker struct {
	client       *Client
	ctx          context.Context
	cancel       context.CancelFunc
	done, wake   chan struct{}
	mu           sync.Mutex
	jobs         []noteProcessingJob
	active       *noteProcessingJob
	activeCancel context.CancelFunc
	completed    map[noteProcessingKey]bool
	order        []noteProcessingKey
	status       NoteProcessingStatus
	work         noteWorkTelemetry
}

func (c *Client) NoteProcessingChanges() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.noteProcessingChanges
}
func (c *Client) NoteProcessingStatus() NoteProcessingStatus {
	if c == nil {
		return NoteProcessingStatus{State: "idle"}
	}
	c.noteProcessingMu.Lock()
	w := c.noteProcessing
	c.noteProcessingMu.Unlock()
	if w == nil {
		return NoteProcessingStatus{State: "idle"}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	status := w.status
	status.Pending = len(w.jobs)
	if w.active != nil {
		status.Pending++
	}
	return status
}
func (c *Client) noteProcessingChanged() {
	select {
	case c.noteProcessingChanges <- struct{}{}:
	default:
	}
}

// Caller holds aiPolicyMu. Only the selected service's configured extractor is
// used, with the same independent consent required for any remote note payload.
func (c *Client) noteProcessingAllowed(service *notes.Service) bool {
	if c.localAI == nil || service == nil {
		return false
	}
	c.localAI.mu.RLock()
	settings, selected, repair := c.localAI.settings, c.localAI.service, c.localAI.needsRepair
	c.localAI.mu.RUnlock()
	if !settings.Enabled || repair || selected != service || !service.AIConfigured() {
		return false
	}
	return !isRemoteCompletionProvider(effectiveProvider(settings)) || settings.RemoteNotesEnabled
}

// Queue identifiers only, never raw note text. Explicit TODO markers already
// have durable suggestions, so they do not trigger a second extraction call.
func (c *Client) queueNoteProcessing(ctx context.Context, result notes.Result, epoch uint64) notes.ProcessingState {
	if len(result.Proposals) > 0 || result.Processing != notes.ProcessingDeterministic {
		return result.Processing
	}
	service, ok := c.notes.(*notes.Service)
	if !ok {
		return result.Processing
	}
	c.aiPolicyMu.RLock()
	defer c.aiPolicyMu.RUnlock()
	if !c.noteProcessingAllowed(service) {
		return result.Processing
	}
	if ctx.Err() != nil || !c.MemoryReadCurrent(epoch) {
		return notes.ProcessingSuggestionsUnavailable
	}
	c.lifeMu.Lock()
	if c.lifeClosed {
		c.lifeMu.Unlock()
		return notes.ProcessingSuggestionsUnavailable
	}
	c.noteProcessingMu.Lock()
	w := c.noteProcessing
	if w == nil {
		life, cancel := context.WithCancel(c.lifeCtx)
		w = &noteProcessingWorker{client: c, ctx: life, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1), completed: make(map[noteProcessingKey]bool), status: NoteProcessingStatus{State: "idle"}}
		c.noteProcessing = w
		go w.run()
	}
	c.noteProcessingMu.Unlock()
	c.lifeMu.Unlock()
	job := noteProcessingJob{key: noteProcessingKey{sourceID: result.SourceID, memoryEpoch: epoch, policyEpoch: c.aiPolicyEpoch}, service: service}
	state := notes.ProcessingSuggestionsUnavailable
	changed := false
	if err := c.CommitDerivedMemory(epoch, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		state, changed = w.enqueue(job, result.Processing)
		return nil
	}); err != nil {
		return notes.ProcessingSuggestionsUnavailable
	}
	if changed {
		c.noteProcessingChanged()
	}
	if state == notes.ProcessingQueued {
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
	return state
}

func (w *noteProcessingWorker) enqueue(job noteProcessingJob, receiptState notes.ProcessingState) (notes.ProcessingState, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ctx.Err() != nil {
		return notes.ProcessingSuggestionsUnavailable, false
	}
	if w.completed[job.key] {
		return receiptState, false
	}
	if w.active != nil && w.active.key == job.key {
		return notes.ProcessingQueued, false
	}
	for _, queued := range w.jobs {
		if queued.key == job.key {
			return notes.ProcessingQueued, false
		}
	}
	count := len(w.jobs)
	if w.active != nil {
		count++
	}
	if count >= maxNoteProcessingJobs {
		w.status = NoteProcessingStatus{State: "unavailable", ErrorCode: "queue_full"}
		w.work.lastErrorCode, w.work.lastErrorAt = "queue_full", time.Now().UTC()
		return notes.ProcessingSuggestionsUnavailable, true
	}
	w.jobs = append(w.jobs, job)
	w.status = NoteProcessingStatus{State: "queued"}
	if w.active != nil {
		w.status.State = "processing"
	}
	return notes.ProcessingQueued, true
}

func (c *Client) invalidateNoteProcessing() {
	c.noteProcessingMu.Lock()
	w := c.noteProcessing
	c.noteProcessingMu.Unlock()
	if w == nil {
		return
	}
	w.mu.Lock()
	w.jobs = nil
	w.work.deferred = false
	clear(w.completed)
	w.order = nil
	cancel := w.activeCancel
	w.status = NoteProcessingStatus{State: "idle"}
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.noteProcessingChanged()
}
func (c *Client) closeNoteProcessing() {
	c.noteProcessingMu.Lock()
	w := c.noteProcessing
	c.noteProcessingMu.Unlock()
	if w == nil {
		return
	}
	w.cancel()
	c.invalidateNoteProcessing()
	<-w.done // Keep the actual extractor slot owned until its call has exited.
}

func (w *noteProcessingWorker) run() {
	defer close(w.done)
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-w.wake:
		}
		for {
			w.mu.Lock()
			if w.ctx.Err() != nil || len(w.jobs) == 0 {
				w.mu.Unlock()
				break
			}
			job := w.jobs[0]
			w.jobs = w.jobs[1:]
			w.active = &job
			w.work.deferred = false
			w.status = NoteProcessingStatus{State: "processing"}
			w.mu.Unlock()
			w.client.noteProcessingChanged()
			w.process(job)
		}
	}
}

func (w *noteProcessingWorker) process(job noteProcessingJob) {
	c := w.client
	ctx, done, err := c.beginOperation(w.ctx, noteOperationTimeout)
	if err != nil {
		w.finish(job, "", false)
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	c.aiPolicyMu.RLock()
	valid := c.aiPolicyEpoch == job.key.policyEpoch && c.noteProcessingAllowed(job.service)
	var id uint64
	if valid {
		id, valid = c.registerAskEpoch(cancel, &job.key.memoryEpoch)
	}
	c.aiPolicyMu.RUnlock()
	if !valid {
		cancel()
		done()
		w.finish(job, "", false)
		return
	}
	w.mu.Lock()
	w.activeCancel = cancel
	w.mu.Unlock()
	ctx = model.WithBackgroundInferenceGroup(ctx, "notes")
	result, err := job.service.ProcessSavedNote(ctx, job.key.sourceID, func(write func() error) error {
		c.aiPolicyMu.RLock()
		defer c.aiPolicyMu.RUnlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		if c.aiPolicyEpoch != job.key.policyEpoch || !c.noteProcessingAllowed(job.service) {
			return context.Canceled
		}
		return c.CommitDerivedMemory(job.key.memoryEpoch, write)
	})
	deferred := errors.Is(err, model.ErrBackgroundPreempted) && ctx.Err() == nil
	c.unregisterAsk(id)
	cancel()
	done()
	if deferred {
		c.aiPolicyMu.RLock()
		retained := false
		if w.ctx.Err() == nil && c.aiPolicyEpoch == job.key.policyEpoch && c.noteProcessingAllowed(job.service) {
			_ = c.CommitDerivedMemory(job.key.memoryEpoch, func() error { w.deferJob(job); retained = true; return nil })
		}
		c.aiPolicyMu.RUnlock()
		if retained {
			c.noteProcessingChanged()
			// Bounded backoff prevents a broken provider repeatedly reporting a
			// deferral from spinning. Actual Apple retries wait behind foreground.
			timer := time.NewTimer(250 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-w.ctx.Done():
			case <-timer.C:
			}
			return
		}
		w.finish(job, "", false)
		return
	}
	code := noteProcessingError(result, err)
	// Publication follows the same lock order as the guarded write, but a
	// current job timeout is still a visible suggestion error, never save loss.
	c.aiPolicyMu.RLock()
	published := false
	if w.ctx.Err() == nil && c.aiPolicyEpoch == job.key.policyEpoch && c.noteProcessingAllowed(job.service) {
		_ = c.CommitDerivedMemory(job.key.memoryEpoch, func() error { w.finish(job, code, true); published = true; return nil })
	}
	c.aiPolicyMu.RUnlock()
	if !published {
		w.finish(job, "", false)
	} else {
		c.noteProcessingChanged()
	}
}

func (w *noteProcessingWorker) finish(job noteProcessingJob, code string, publish bool) {
	w.mu.Lock()
	w.active, w.activeCancel = nil, nil
	w.work.deferred = false
	if !publish && w.status.State == "processing" {
		w.status = NoteProcessingStatus{State: "idle"}
		if len(w.jobs) > 0 {
			w.status.State = "queued"
		}
	}
	if publish {
		w.status = NoteProcessingStatus{State: "ready"}
		if code != "" {
			w.work.lastErrorCode, w.work.lastErrorAt = code, time.Now().UTC()
			w.status = NoteProcessingStatus{State: "unavailable", ErrorCode: code}
		} else {
			w.work.lastCompletedAt = time.Now().UTC()
			if !w.completed[job.key] {
				w.work.completed++
				w.completed[job.key] = true
				w.order = append(w.order, job.key)
			}
			if len(w.order) > maxCompletedNoteJobs {
				delete(w.completed, w.order[0])
				w.order = w.order[1:]
			}
			if len(w.jobs) > 0 {
				w.status.State = "queued"
			}
		}
	}
	w.mu.Unlock()
}

func noteProcessingError(result notes.Result, err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, notes.ErrSourceUnavailable):
		return "source_unavailable"
	case err != nil:
		return "suggestions_unavailable"
	case result.Processing == notes.ProcessingBudget:
		return "model_budget_exceeded"
	case result.Processing == notes.ProcessingInvalidOutput:
		return "invalid_output"
	case result.Processing != notes.ProcessingModel:
		return "model_unavailable"
	default:
		return ""
	}
}

// Caller holds current policy+memory publication fences. The active job keeps
// its one bounded membership slot; no source is resaved or marked completed.
func (w *noteProcessingWorker) deferJob(job noteProcessingJob) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.active, w.activeCancel = nil, nil
	w.jobs = append(w.jobs, noteProcessingJob{})
	copy(w.jobs[1:], w.jobs[:len(w.jobs)-1])
	w.jobs[0] = job
	w.work.deferred = true
	w.status = NoteProcessingStatus{State: "queued"}
}
