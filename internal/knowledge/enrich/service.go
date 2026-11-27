package enrich

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/backgroundrepair"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/model"
	"github.com/hazcod/blur/internal/power"
	"github.com/hazcod/blur/internal/storage"
)

var ErrMoreWork = errors.New("library enrichment: more work")
var ErrPolicy = errors.New("library enrichment: paused by power settings")

type Generator interface {
	derive.Generator
	Identity(context.Context) (string, error)
}

type compactingGenerator interface {
	CountCompactedInputTokens(context.Context, []derive.Evidence, int) (int, error)
	GenerateCompacted(context.Context, []derive.Evidence, int) ([]derive.Proposal, string, error)
}

type Policy interface {
	Effective(context.Context) (power.Effective, error)
}

// Fence makes the brief publication atomic relative to a user deletion window.
// Model work runs outside this fence; cancellation alone cannot fence a provider
// that ignores its context.
type Fence interface {
	BeginMemoryRead() (uint64, bool)
	CommitDerivedMemory(uint64, func() error) error
}

type Options struct {
	Policy   Policy
	Fence    Fence
	OnChange func()
}

type Status struct {
	State           string
	ErrorCode       string
	Complete        bool
	ProcessedChunks int
	Active          bool
	Queued          int // Known incremental source IDs only; backfill total is unknown.
	DeferredReason  string
	LastCompletedAt time.Time
	LastErrorCode   string
	LastErrorAt     time.Time
	CoverageLimited bool // At least one current-model source has a declined tail.
}

// Service queues identifiers, never captured text, and resumes its independent
// legacy/multipart directories and long sources from encrypted cursors.
type Service struct {
	sources         Sources
	repository      Repository
	generator       Generator
	options         Options
	ctx             context.Context
	cancel          context.CancelFunc
	runMu           sync.Mutex
	mu              sync.Mutex
	wake            chan struct{}
	done            chan struct{}
	stopped         chan struct{}
	started, closed bool
	runCancel       context.CancelFunc
	pending         map[string]bool
	pendingAfter    string // Round-robin admission only; source cursors remain durable.
	repair          bool
	retryInvalid    bool
	repairAfterPass bool
	capturesDirty   bool
	repairInterval  time.Duration // Zero uses the normal ten-minute repair interval.
	status          Status
}

func NewService(sources Sources, repository Repository, generator Generator, options Options) (*Service, error) {
	if sources == nil || repository == nil || !repository.EncryptedAtRest() || generator == nil {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{sources: sources, repository: repository, generator: generator, options: options, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}), stopped: make(chan struct{}), pending: map[string]bool{}, capturesDirty: true, status: Status{State: "indexing"}}, nil
}

func (s *Service) changed() {
	if s.options.OnChange != nil {
		s.options.OnChange()
	}
}
func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Service) Start() {
	s.mu.Lock()
	if s.started || s.closed {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()
	go s.run()
	s.signal()
}
func (s *Service) Wake() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.repair = true
	s.mu.Unlock()
	s.signal()
}

// RetryInvalidOutputs admits one bounded repair pass. Refused sources remain
// terminal; failed output is retried only after this explicit request.
func (s *Service) RetryInvalidOutputs() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.repair, s.retryInvalid = true, true
	s.mu.Unlock()
	s.signal()
}

// WakeCaptures checks only the append-ordered multipart tail. Routine capture
// events must not invoke Wake's explicit full-directory repair semantics.
func (s *Service) WakeCaptures() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.capturesDirty = true
	s.mu.Unlock()
	s.signal()
}
func (s *Service) NotifySource(id string) {
	if s == nil || !ValidHash(id) {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if len(s.pending) < 1024 {
		s.pending[id] = true
	} else {
		s.repair = true
	}
	s.mu.Unlock()
	s.signal()
}
func (s *Service) Invalidate() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.runCancel != nil {
		s.runCancel()
	}
	clear(s.pending)
	s.pendingAfter = ""
	s.repair = true
	s.status.Complete = false
	s.mu.Unlock()
}
func (s *Service) Status() Status {
	if s == nil {
		return Status{State: "disabled"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.status
	status.Active = s.runCancel != nil
	status.Queued = len(s.pending)
	return status
}
func (s *Service) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.stopped
		return
	}
	s.closed = true
	s.cancel()
	if s.runCancel != nil {
		s.runCancel()
	}
	started := s.started
	s.mu.Unlock()
	if started {
		<-s.done
	}
	s.runMu.Lock()
	if closer, ok := s.generator.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
	s.runMu.Unlock()
	close(s.stopped)
}

func (s *Service) run() {
	defer close(s.done)
	interval := s.repairInterval
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	timer := time.NewTimer(interval)
	repairs := backgroundrepair.New(time.Now(), interval)
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.wake:
		case <-timer.C:
		}
		periodic := repairs.Due(time.Now())
		if periodic {
			s.mu.Lock()
			s.repair = true
			s.mu.Unlock()
		}
		ctx, cancel := context.WithTimeout(s.ctx, 2*time.Minute)
		admitted := false
		err := s.backfill(ctx, &admitted)
		cancel()
		if periodic && admitted {
			repairs.Admitted(time.Now())
		}
		delay := interval
		if errors.Is(err, ErrMoreWork) || errors.Is(err, context.DeadlineExceeded) {
			delay = time.Second
		} else if err != nil {
			delay = time.Minute
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(repairs.Delay(time.Now(), delay))
	}
}

func (s *Service) effective(ctx context.Context) (power.Effective, error) {
	var policy power.Effective
	var err error
	if s.options.Policy != nil {
		policy, err = s.options.Policy.Effective(ctx)
	} else {
		policy, err = power.Resolve(power.DefaultConfig(), power.SystemState{})
	}
	if err != nil {
		return power.Effective{}, err
	}
	if !policy.OptionalIndexing || policy.MaxBackgroundAIJobs < 1 {
		return power.Effective{}, ErrPolicy
	}
	return policy, nil
}

func (s *Service) commit(epoch uint64, fn func() error) error {
	if s.options.Fence != nil {
		return s.options.Fence.CommitDerivedMemory(epoch, fn)
	}
	return fn()
}

// Backfill performs at most four model chunks per quantum, preserving progress
// before yielding. A source is not marked covered after reading only its prefix.
func (s *Service) Backfill(parent context.Context) (result error) {
	return s.backfill(parent, nil)
}

func (s *Service) backfill(parent context.Context, repairAdmitted *bool) (result error) {
	if s == nil || parent == nil {
		return ErrInvalid
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	ctx, cancel := context.WithCancel(model.WithBackgroundInferenceGroup(parent, "library"))
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrUnavailable
	}
	s.runCancel = cancel
	s.status.State = "indexing"
	s.status.Complete = false
	s.status.ErrorCode = ""
	s.status.DeferredReason = ""
	repair := s.repair
	retryInvalid := s.retryInvalid
	s.retryInvalid = false
	repairConsumed := false
	s.repair = false
	capturesDirty := s.capturesDirty
	s.capturesDirty = false
	s.mu.Unlock()
	defer func() {
		deferredForForeground := errors.Is(result, model.ErrBackgroundPreempted) && ctx.Err() == nil
		if errors.Is(result, model.ErrBackgroundPreempted) {
			if err := ctx.Err(); err != nil {
				result = err
			} else {
				result = ErrMoreWork
			}
		}
		s.mu.Lock()
		s.runCancel = nil
		if repair && !repairConsumed && result != nil {
			s.repair = true
			s.retryInvalid = s.retryInvalid || retryInvalid
		}
		if capturesDirty && result != nil {
			s.capturesDirty = true
		}
		if errors.Is(result, context.DeadlineExceeded) {
			// The existing retry policy still resumes this quantum. Preserve
			// the observed timeout as history rather than claiming no issue.
			s.status.LastErrorCode = "timeout"
			s.status.LastErrorAt = time.Now().UTC()
		}
		if deferredForForeground {
			s.status.DeferredReason = "foreground"
		}
		if result == nil && (len(s.pending) > 0 || s.repair || s.repairAfterPass || s.capturesDirty) {
			result = ErrMoreWork
		}
		if result == nil {
			s.status.State = "ready"
			s.status.Complete = true
		} else if errors.Is(result, ErrMoreWork) || errors.Is(result, context.DeadlineExceeded) {
			s.status.State = "indexing"
		} else if errors.Is(result, ErrPolicy) {
			s.status.State = "paused"
			s.status.DeferredReason = "policy"
		} else if errors.Is(result, context.Canceled) {
			s.status.State = "indexing"
		} else {
			s.status.State = "unavailable"
			s.status.ErrorCode = errorCode(result)
			s.status.LastErrorCode = s.status.ErrorCode
			s.status.LastErrorAt = time.Now().UTC()
		}
		s.mu.Unlock()
		s.changed()
	}()
	var epoch uint64
	if s.options.Fence != nil {
		var stable bool
		epoch, stable = s.options.Fence.BeginMemoryRead()
		if !stable {
			return context.Canceled
		}
	}
	if _, err := s.effective(ctx); err != nil {
		return err
	}
	digest, err := s.generator.Identity(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(digest) == "" || len(digest) > 256 {
		return ErrInvalid
	}
	key := ModelKey(digest)
	checkpoint, found, err := s.repository.GetEnrichmentCheckpoint(ctx)
	if err != nil {
		return err
	}
	sameModelCheckpoint := found && checkpoint.ModelKey == key
	if !sameModelCheckpoint {
		checkpoint = Checkpoint{ModelKey: key}
	}
	if checkpoint.OutputRevision < OutputDecoderRevision {
		if sameModelCheckpoint && checkpoint.CoverageLimited {
			// Revisit durable invalid-output receipts once with this decoder.
			// process skips complete sources and native refusals, preserving
			// their work and the ordinary four-chunk inference quantum.
			checkpoint.After, checkpoint.CaptureAfter = "", ""
			checkpoint.Complete, checkpoint.CaptureComplete = false, false
			checkpoint.CoverageLimited = false
		}
		checkpoint.OutputRevision = OutputDecoderRevision
		if err := s.commit(epoch, func() error { return s.repository.PutEnrichmentCheckpoint(ctx, checkpoint) }); err != nil {
			return err
		}
	}
	if capturesDirty {
		checkpoint.CaptureComplete = false
	}
	if retryInvalid {
		checkpoint.RetryInvalid = true
	}
	if checkpoint.Complete && repair {
		checkpoint.After = ""
		checkpoint.Complete = false
		checkpoint.CoverageLimited = false
		checkpoint.CaptureAfter = ""
		checkpoint.CaptureComplete = false
		// Persist repair admission before processing a source. An error in
		// the first source must neither lose the reset nor restart it later.
		if err := s.commit(epoch, func() error { return s.repository.PutEnrichmentCheckpoint(ctx, checkpoint) }); err != nil {
			return err
		}
		repairConsumed = true
	} else if repair {
		s.mu.Lock()
		s.repairAfterPass = true
		s.mu.Unlock()
		repairConsumed = true
		if retryInvalid {
			if err := s.commit(epoch, func() error { return s.repository.PutEnrichmentCheckpoint(ctx, checkpoint) }); err != nil {
				repairConsumed = false
				return err
			}
		}
	}
	if repairConsumed && repairAdmitted != nil {
		*repairAdmitted = true
	}
	s.mu.Lock()
	s.status.CoverageLimited = checkpoint.CoverageLimited
	s.mu.Unlock()
	budget := 4
	// Incremental work is independent of the backfill cursor: a new source may
	// sort before the current source. Leave it queued until fully committed.
	s.mu.Lock()
	pending := make([]string, 0, len(s.pending))
	for id := range s.pending {
		pending = append(pending, id)
	}
	pendingAfter := s.pendingAfter
	s.mu.Unlock()
	sort.Strings(pending)
	// Give independently captured work a turn before revisiting a large
	// source. A bounded incremental share also reserves service for durable
	// legacy and multipart directories under a continuous capture stream.
	pivot := sort.Search(len(pending), func(i int) bool { return pending[i] > pendingAfter })
	pending = append(pending[pivot:], pending[:pivot]...)
	pendingLimit := 2
	_, hasCaptureDirectory := s.sources.(storage.CaptureEvidencePageReader)
	if checkpoint.Complete && (checkpoint.CaptureComplete || !hasCaptureDirectory) {
		// No competing directory work is known, so spend otherwise idle
		// capacity on the incremental queue without changing its rotation.
		pendingLimit = budget
	}
	pendingUsed := 0
	for visited, id := range pending {
		if visited >= 32 || pendingUsed >= pendingLimit {
			break
		}
		slice := 1
		complete, err := s.process(ctx, epoch, key, digest, id, &slice, checkpoint.RetryInvalid)
		s.mu.Lock()
		if ctx.Err() == nil {
			s.pendingAfter = id
		}
		s.mu.Unlock()
		used := 1 - slice
		pendingUsed += used
		budget -= used
		if err != nil {
			return err
		}
		if limited := s.Status().CoverageLimited; limited != checkpoint.CoverageLimited {
			checkpoint.CoverageLimited = limited
			if err := s.commit(epoch, func() error { return s.repository.PutEnrichmentCheckpoint(ctx, checkpoint) }); err != nil {
				return err
			}
		}
		if complete {
			s.mu.Lock()
			delete(s.pending, id)
			s.mu.Unlock()
		}
	}
	if checkpoint.Complete {
		return s.backfillCaptures(ctx, epoch, key, digest, &checkpoint, &budget)
	}
	// Appended capture parts must advance even while an old legacy source
	// needs many quanta. One chunk here leaves at least one for legacy work.
	captureSlice := 1
	captureErr := s.backfillCaptures(ctx, epoch, key, digest, &checkpoint, &captureSlice)
	budget -= 1 - captureSlice
	if captureErr != nil && !errors.Is(captureErr, ErrMoreWork) {
		return captureErr
	}
	// Bound scanning too: already-covered captures require no model invocation.
	for scanned := 0; scanned < 32; {
		page, err := s.sources.ListObservationPage(ctx, storage.ObservationPageRequest{After: checkpoint.After, Limit: 4, MaxBytes: storage.MaxObservationPageBytes})
		if err != nil {
			return err
		}
		for _, row := range page.Observations {
			id := row.ContentID()
			complete, err := s.process(ctx, epoch, key, digest, id, &budget, checkpoint.RetryInvalid)
			if err != nil {
				return err
			}
			if !complete {
				return ErrMoreWork
			}
			checkpoint.After = id
			checkpoint.CoverageLimited = s.Status().CoverageLimited
			if err := s.commit(epoch, func() error { return s.repository.PutEnrichmentCheckpoint(ctx, checkpoint) }); err != nil {
				return err
			}
			scanned++
			if budget == 0 {
				return ErrMoreWork
			}
		}
		if !page.HasMore {
			s.mu.Lock()
			repeat := s.repairAfterPass || s.repair
			s.repairAfterPass, s.repair = false, false
			pending := len(s.pending) > 0
			s.mu.Unlock()
			if repeat {
				repair, repairConsumed = true, false
				checkpoint.After = ""
				checkpoint.Complete = false
				checkpoint.CoverageLimited = false
				checkpoint.CaptureAfter = ""
				checkpoint.CaptureComplete = false
				s.mu.Lock()
				s.status.CoverageLimited = false
				s.mu.Unlock()
				if err := s.commit(epoch, func() error { return s.repository.PutEnrichmentCheckpoint(ctx, checkpoint) }); err != nil {
					return err
				}
				repairConsumed = true
				return ErrMoreWork
			}
			checkpoint.Complete = true
			checkpoint.CoverageLimited = s.Status().CoverageLimited
			if err := s.commit(epoch, func() error { return s.repository.PutEnrichmentCheckpoint(ctx, checkpoint) }); err != nil {
				return err
			}
			if pending {
				return ErrMoreWork
			}
			return s.backfillCaptures(ctx, epoch, key, digest, &checkpoint, &budget)
		}
		if len(page.Observations) == 0 {
			return ErrInvalid
		}
	}
	return ErrMoreWork
}

// The optional multipart directory has its own publication-scoped cursor.
// Legacy backends keep their existing ordering and checkpoint meaning.
func (s *Service) backfillCaptures(ctx context.Context, epoch uint64, key, digest string, checkpoint *Checkpoint, budget *int) error {
	reader, ok := s.sources.(storage.CaptureEvidencePageReader)
	if !ok || checkpoint.CaptureComplete {
		if checkpoint.Complete && checkpoint.RetryInvalid {
			checkpoint.RetryInvalid = false
			return s.commit(epoch, func() error { return s.repository.PutEnrichmentCheckpoint(ctx, *checkpoint) })
		}
		return nil
	}
	for scanned := 0; scanned < 32; {
		before := checkpoint.CaptureAfter
		page, err := reader.ListCaptureEvidencePage(ctx, storage.CaptureEvidencePageRequest{After: checkpoint.CaptureAfter, Limit: 4, MaxBytes: storage.MaxObservationPageBytes})
		if errors.Is(err, storage.ErrCaptureCursorChanged) {
			checkpoint.CaptureAfter, checkpoint.CaptureComplete = "", false
			if err := s.commit(epoch, func() error { return s.repository.PutEnrichmentCheckpoint(ctx, *checkpoint) }); err != nil {
				return err
			}
			return ErrMoreWork
		}
		if err != nil {
			return err
		}
		for _, source := range page.Sources {
			provenance, multipart := source.Provenance()
			if source.Validate() != nil || !multipart {
				return ErrInvalid
			}
			complete, err := s.process(ctx, epoch, key, digest, source.SourceID(), budget, checkpoint.RetryInvalid)
			if err != nil {
				return err
			}
			if !complete {
				return ErrMoreWork
			}
			checkpoint.CaptureAfter = fmt.Sprintf("%s:%s:%d", provenance.EditionID, provenance.Publication, provenance.PartIndex)
			checkpoint.CoverageLimited = s.Status().CoverageLimited
			if err := s.commit(epoch, func() error { return s.repository.PutEnrichmentCheckpoint(ctx, *checkpoint) }); err != nil {
				return err
			}
			scanned++
			if *budget == 0 {
				return ErrMoreWork
			}
		}
		if !storage.ValidCaptureEvidenceCursor(page.Next) {
			return ErrInvalid
		}
		// A bounded page may also advance past editions hidden by an erasure
		// fence. No row in this page remains unprocessed at this point.
		if page.Next != checkpoint.CaptureAfter {
			checkpoint.CaptureAfter = page.Next
			if err := s.commit(epoch, func() error { return s.repository.PutEnrichmentCheckpoint(ctx, *checkpoint) }); err != nil {
				return err
			}
		}
		if page.Pending {
			return ErrMoreWork // Published text is waiting for its bounded index worker.
		}
		if !page.HasMore {
			checkpoint.CaptureComplete = true
			if checkpoint.Complete {
				checkpoint.RetryInvalid = false
			}
			if !checkpoint.Complete && checkpoint.CaptureAfter == "" {
				// An empty directory changed no source cursor. Carry this
				// no-work result into the next legacy checkpoint rather than
				// publishing another indistinguishable repair-reset receipt.
				return nil
			}
			return s.commit(epoch, func() error { return s.repository.PutEnrichmentCheckpoint(ctx, *checkpoint) })
		}
		if len(page.Sources) == 0 {
			if checkpoint.CaptureAfter == before {
				return ErrInvalid
			}
			scanned++ // Bound pages that only skipped inaccessible editions too.
		}
	}
	return ErrMoreWork
}

func (s *Service) process(ctx context.Context, epoch uint64, key, digest, id string, budget *int, retryInvalid bool) (bool, error) {
	progress, found, err := s.repository.GetEnrichmentProgress(ctx, id)
	if err != nil {
		return false, err
	}
	retrying := progress.ModelKey == key && progress.Declined && progress.FailureCode == FailureInvalidOutput && (retryInvalid || progress.OutputRevision < OutputDecoderRevision)
	_, compacts := s.generator.(compactingGenerator)
	if found && (progress.Suppressed && !compacts || progress.ModelKey == key && (progress.Complete || progress.Declined && !retrying)) {
		if progress.ModelKey == key && progress.ClaimsLimited {
			s.mu.Lock()
			s.status.CoverageLimited = true
			s.mu.Unlock()
		}
		if !progress.Suppressed && progress.ModelKey == key && progress.Declined {
			s.recordDeclined(false, progress.FailureCode)
		}
		return true, nil
	}
	if current, ok := s.sources.(interface {
		IsCurrentNoteSource(context.Context, string) (bool, error)
	}); ok {
		live, err := current.IsCurrentNoteSource(ctx, id)
		if err != nil || !live {
			return true, err
		}
	}
	source, _, found, err := storage.ReadSourceEvidenceWithCaptureTime(ctx, s.sources, id)
	if err != nil || !found {
		return true, err
	}
	start := 0
	if progress.ModelKey == key {
		start = progress.Next
	}
	for *budget > 0 {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		policy, err := s.effective(ctx)
		if err != nil {
			return false, err
		}
		end, next, complete, err := NextChunk(source.Text(), start)
		if err != nil {
			return false, err
		}
		chunk := Chunk{SourceID: id, ModelKey: key, Start: start, End: end, Next: next, Complete: complete, RetryInvalid: retrying, OutputRevision: OutputDecoderRevision}
		compactor, compacting := s.generator.(compactingGenerator)
		tokens := 0
		outputTokens := derive.MaxOutputTokens
		if compacting && source.AppID() != appidentity.BundleID {
			chunk, tokens, outputTokens, err = s.admitCompaction(ctx, compactor, policy, source.Text(), chunk)
			if err != nil {
				return false, err
			}
			end, next, complete = chunk.End, chunk.Next, chunk.Complete
			if mode, ok := compactor.(interface{ ExtractsClaims() bool }); ok {
				chunk.PreserveClaims = !mode.ExtractsClaims()
			}
		}
		text := strings.TrimSpace(source.Text()[start:end])
		if text != "" && source.AppID() != appidentity.BundleID {
			input := []derive.Evidence{{SourceID: id, Text: text}}
			if !compacting {
				tokens, err = s.generator.CountInputTokens(ctx, input)
			}
			if err != nil {
				return false, err
			}
			if tokens < 1 || tokens > model.ContextTokenLimit-model.PromptTemplateReserve-outputTokens {
				return false, ErrCapacity
			}
			capacity, err := model.RuntimeContextLimit(ctx, s.generator, model.ContextTokenLimit)
			if err != nil {
				return false, err
			}
			if tokens+outputTokens+model.PromptTemplateReserve > capacity {
				return false, ErrCapacity
			}
			duration := min(policy.MaxBackgroundDuration, time.Minute)
			if !policy.AllowBackgroundAI(power.LocalProvider, tokens+outputTokens, duration) {
				return false, ErrPolicy
			}
			jobCtx, cancel := context.WithTimeout(ctx, duration)
			if compacting {
				chunk.Proposals, chunk.Compaction, err = compactor.GenerateCompacted(jobCtx, input, outputTokens)
				if err == nil {
					chunk.CompactionVersion = 1
					if !ValidCompaction(1, chunk.Compaction) {
						err = derive.ErrInvalid
					}
				}
			} else {
				chunk.Proposals, err = s.generator.Generate(jobCtx, input)
			}
			declined := errors.Is(err, model.ErrAppleRefused)
			invalid := invalidOutput(err)
			if err == nil && derive.ValidateSuggestions(input, chunk.Proposals) != nil {
				invalid = true
				err = derive.ErrInvalid
			}
			if (err == nil || declined || invalid) && jobCtx.Err() == nil {
				var after string
				after, err = s.generator.Identity(jobCtx)
				if err == nil && after != digest {
					err = model.ErrOllamaModelChanged
				}
			}
			jobErr := jobCtx.Err()
			cancel()
			if jobErr != nil && !errors.Is(err, model.ErrBackgroundPreempted) {
				return false, jobErr
			}
			if err != nil {
				return false, err
			}
			chunk.Declined = declined || invalid
			if invalid {
				chunk.FailureCode = FailureInvalidOutput
			}
			if chunk.Declined {
				chunk.Proposals = nil
				chunk.Compaction, chunk.CompactionVersion = "", 0
			} else if derive.ValidateSuggestions(input, chunk.Proposals) != nil {
				return false, ErrInvalid
			}
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if _, err := s.effective(ctx); err != nil {
			return false, err
		}
		err = s.commit(epoch, func() error { return s.repository.ApplyEnrichment(ctx, chunk) })
		if errors.Is(err, ErrSourceGone) || errors.Is(err, ErrSuppressed) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if chunk.CompactionVersion != 0 {
			stored, _, readErr := s.repository.GetEnrichmentProgress(ctx, id)
			if readErr != nil {
				return false, readErr
			}
			if stored.ClaimsLimited {
				s.mu.Lock()
				s.status.CoverageLimited = true
				s.mu.Unlock()
			}
		}
		*budget--
		if chunk.Declined {
			s.recordDeclined(true, chunk.FailureCode)
			s.changed()
			return true, nil
		}
		retrying = false
		s.mu.Lock()
		s.status.ProcessedChunks++
		s.status.LastCompletedAt = time.Now().UTC()
		s.mu.Unlock()
		s.changed()
		if complete {
			return true, nil
		}
		start = next
	}
	return false, nil
}

func invalidOutput(err error) bool {
	return errors.Is(err, model.ErrAppleResponse) || errors.Is(err, model.ErrOllamaResponse) || errors.Is(err, derive.ErrInvalid)
}

func (s *Service) recordDeclined(fresh bool, failureCode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.CoverageLimited = true
	if fresh {
		if failureCode == "" {
			failureCode = "model_refused"
		}
		s.status.LastErrorCode = failureCode
		s.status.LastErrorAt = time.Now().UTC()
	}
}

func errorCode(err error) string {
	switch {
	case errors.Is(err, model.ErrAppleUnavailable), errors.Is(err, model.ErrOllamaUnavailable):
		return "provider_unavailable"
	case errors.Is(err, model.ErrAppleBudget), errors.Is(err, model.ErrOllamaBudget):
		return "model_budget_exceeded"
	case errors.Is(err, model.ErrAppleResponse), errors.Is(err, model.ErrOllamaResponse), errors.Is(err, derive.ErrInvalid):
		return "invalid_output"
	case errors.Is(err, model.ErrAppleRefused):
		return "model_refused"
	case errors.Is(err, model.ErrAppleConfig):
		return "model_configuration"
	case errors.Is(err, model.ErrOllamaRemoteModel):
		return "local_model_required"
	case errors.Is(err, model.ErrOllamaModelMissing):
		return "model_missing"
	case errors.Is(err, model.ErrOllamaModelChanged):
		return "model_changed"
	case errors.Is(err, ErrCapacity):
		return "capacity"
	default:
		return "local_processing_failed"
	}
}
