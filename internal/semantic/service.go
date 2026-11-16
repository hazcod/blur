package semantic

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/backgroundrepair"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/model"
	"github.com/hazcod/blur/internal/power"
	"github.com/hazcod/blur/internal/storage"
)

var ErrPolicy = errors.New("semantic: background work paused by power policy")
var errMoreWork = errors.New("semantic: backfill has more sources")

const (
	// Candidate ranking is an explicit, bounded recall operation. Saved notes
	// fit in one candidate; passage splitting keeps their tail searchable.
	MaxRankCandidates     = 256
	MaxRankCandidateBytes = 16 << 10
	MaxRankInputBytes     = 1 << 20
	maxRankDuration       = 30 * time.Second
)

// SourceReader retains legacy backends; published multipart readers also expose
// storage.SourceEvidenceTimeReader and storage.CaptureEvidencePageReader.
type SourceReader interface {
	ListObservationPage(context.Context, storage.ObservationPageRequest) (storage.ObservationPage, error)
}

type incrementalSourceReader interface {
	GetObservationWithCaptureTime(context.Context, string) (capture.SanitizedObservation, time.Time, bool, error)
}

type BackgroundPolicy interface {
	Effective(context.Context) (power.Effective, error)
}

type Options struct {
	Policy   BackgroundPolicy
	CanRead  func() bool
	OnChange func()
}

type Status struct {
	State                                  string
	Model                                  string
	IndexedSources, SkippedSources, Chunks int
	Complete                               bool
	ErrorCode                              string
}

// Service owns one local embedding generation. Encrypted vectors survive
// restarts; the in-memory ANN graph is rebuilt from those records. Every new
// capture can wake a resumable pass without queuing captured text in memory.
type Service struct {
	sources         SourceReader
	repository      Repository
	embedder        Embedder
	options         Options
	ctx             context.Context
	cancel          context.CancelFunc
	wake            chan struct{}
	done            chan struct{}
	stopped         chan struct{}
	queryWG         sync.WaitGroup
	runMu           sync.Mutex
	mu              sync.RWMutex
	started, closed bool
	epoch           uint64
	runCancel       context.CancelFunc
	index           *Index
	identity        Model
	loaded          bool
	loadAfter       string
	status          Status
	queryID         uint64
	queries         map[uint64]context.CancelFunc
	pending         map[string]struct{}
	rescan          bool
	repairAfterPass bool
	capturesDirty   bool
	repairInterval  time.Duration // Zero uses the normal ten-minute repair interval.
}

func NewService(sources SourceReader, repository Repository, embedder Embedder, options Options) (*Service, error) {
	if sources == nil || repository == nil || !repository.EncryptedAtRest() || embedder == nil {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{sources: sources, repository: repository, embedder: embedder, options: options, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}), stopped: make(chan struct{}), status: Status{State: "loading"}, queries: make(map[uint64]context.CancelFunc), pending: make(map[string]struct{}), capturesDirty: true}, nil
}

func (s *Service) Start() {
	s.mu.Lock()
	if s.closed || s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()
	go s.run()
	s.Wake()
}

func (s *Service) Wake() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if s.status.State == "ready" {
		s.status.State = "indexing"
		s.status.Complete = false
	}
	s.rescan = true
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// WakeCaptures checks the append-ordered publication tail without invoking
// Wake's explicit legacy/capture history repair. Persisted vectors still load
// normally after restart; completed source cursors are retained.
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
	if s.status.State == "ready" {
		s.status.State, s.status.Complete = "indexing", false
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// NotifySource queues only a content identifier. The bounded queue never
// retains source text; overflow schedules a complete repair pass instead.
func (s *Service) NotifySource(sourceID string) {
	if s == nil || !ValidID(sourceID) {
		return
	}
	if _, ok := s.sources.(incrementalSourceReader); !ok {
		s.Wake()
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if len(s.pending) < 1024 {
		s.pending[sourceID] = struct{}{}
	} else {
		s.rescan = true
		s.repairAfterPass = true
	}
	if s.status.State == "ready" {
		s.status.State = "indexing"
		s.status.Complete = false
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Invalidate makes cached vectors unreachable before a source mutation starts.
// In-flight jobs and queries are cancelled; publication checks the same epoch.
func (s *Service) Invalidate() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.epoch++
	if s.runCancel != nil {
		s.runCancel()
		s.runCancel = nil
	}
	for _, cancel := range s.queries {
		cancel()
	}
	old := s.index
	s.index = nil
	s.loaded = false
	s.loadAfter = ""
	clear(s.pending)
	s.rescan = true
	s.repairAfterPass = true
	s.status = Status{State: "loading", Model: s.identity.Name}
	s.mu.Unlock()
	if old != nil {
		go old.Purge()
	}
	s.changed()
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
	for _, cancel := range s.queries {
		cancel()
	}
	started := s.started
	s.mu.Unlock()
	if started {
		<-s.done
	}
	s.queryWG.Wait()
	s.runMu.Lock()
	s.mu.Lock()
	old := s.index
	s.index = nil
	s.mu.Unlock()
	if old != nil {
		old.Purge()
	}
	if closer, ok := s.embedder.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
	s.runMu.Unlock()
	close(s.stopped)
}

func (s *Service) Status() Status {
	if s == nil {
		return Status{State: "disabled"}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
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
		ctx, cancel := context.WithTimeout(s.ctx, 2*time.Minute)
		admitted := false
		err := s.backfillWithAdmission(ctx, periodic, &admitted)
		cancel()
		if periodic && admitted {
			repairs.Admitted(time.Now())
		}
		delay := interval
		if errors.Is(err, errMoreWork) || errors.Is(err, context.DeadlineExceeded) {
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

// Backfill performs one bounded source pass quantum. It is also a synchronous
// entry point for deterministic verification. The background loop resumes a
// yielded quantum and preserves source cursors across process restarts.
func (s *Service) Backfill(parent context.Context) (result error) {
	return s.backfill(parent, true)
}

func (s *Service) backfill(parent context.Context, fullScan bool) (result error) {
	return s.backfillWithAdmission(parent, fullScan, nil)
}

func (s *Service) backfillWithAdmission(parent context.Context, fullScan bool, repairAdmitted *bool) (result error) {
	if s == nil || parent == nil {
		return ErrInvalid
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	fullScanAttempt := fullScan
	forceLegacyRepair := fullScan && repairAdmitted != nil
	fullScanConsumed := false
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrUnavailable
	}
	epoch := s.epoch
	s.runCancel = cancel
	capturesDirty := s.capturesDirty
	s.capturesDirty = false
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.epoch == epoch {
			s.runCancel = nil
			if fullScanAttempt && !fullScanConsumed && result != nil {
				s.rescan = true
			}
			if capturesDirty && result != nil {
				s.capturesDirty = true
			}
			if result == nil && s.capturesDirty {
				result = errMoreWork
				s.status.State, s.status.Complete = "indexing", false
			}
			if result != nil && !errors.Is(result, errMoreWork) && !errors.Is(result, context.Canceled) {
				s.status.State = "unavailable"
				s.status.ErrorCode = errorCode(result)
				if errors.Is(result, ErrPolicy) {
					s.status.State = "paused"
				}
				if errors.Is(result, context.DeadlineExceeded) {
					s.status.State = "indexing"
					s.status.ErrorCode = ""
				}
			}
		}
		s.mu.Unlock()
		s.changed()
	}()
	if !s.canRead() {
		return ErrUnavailable
	}
	identity, err := s.embedder.Identity(ctx)
	if err != nil {
		return err
	}
	if identity.Validate() != nil {
		return ErrInvalid
	}
	s.mu.Lock()
	if s.epoch != epoch || ctx.Err() != nil {
		s.mu.Unlock()
		return context.Canceled
	}
	if s.index == nil || s.identity.Key() != identity.Key() {
		old := s.index
		s.index, _ = NewIndex(identity)
		s.identity = identity
		s.loaded = false
		s.loadAfter = ""
		if old != nil {
			go old.Purge()
		}
	}
	index := s.index
	s.status.State = "loading"
	s.status.Model = identity.Name
	s.status.ErrorCode = ""
	s.status.Complete = false
	loaded, after := s.loaded, s.loadAfter
	forceLegacyRepair = forceLegacyRepair || s.rescan
	fullScan = fullScan || s.rescan
	fullScanAttempt = fullScan
	s.rescan = false
	s.mu.Unlock()
	s.changed()
	if !loaded {
		for loadedCount := 0; ; {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !s.canRead() {
				return ErrUnavailable
			}
			page, err := s.repository.ListSemanticDocuments(ctx, PageRequest{After: after, ModelKey: identity.Key(), Limit: MaxPageRows, MaxBytes: MaxPageBytes})
			if err != nil {
				return err
			}
			for _, document := range page.Documents {
				if err := index.AddDocument(ctx, document); err != nil {
					clearDocument(document)
					return err
				}
				clearDocument(document)
				loadedCount++
			}
			if page.HasMore && (page.Next == "" || page.Next == after) {
				return ErrInvalid
			}
			after = page.Next
			s.mu.Lock()
			if s.epoch != epoch {
				s.mu.Unlock()
				return context.Canceled
			}
			s.loadAfter = after
			s.loaded = !page.HasMore
			s.mu.Unlock()
			s.publishCounts(index, epoch, "loading", false)
			if !page.HasMore {
				break
			}
			if loadedCount >= 256 {
				return errMoreWork
			}
		}
	}
	checkpoint, found, err := s.repository.GetSemanticCheckpoint(ctx, identity.Key())
	if err != nil {
		return err
	}
	if reader, ok := s.sources.(incrementalSourceReader); ok {
		for processed := 0; ; processed++ {
			s.mu.RLock()
			var sourceID string
			for id := range s.pending {
				sourceID = id
				break
			}
			s.mu.RUnlock()
			if sourceID == "" {
				break
			}
			if processed >= 64 {
				return errMoreWork
			}
			if !s.current(epoch) || !s.canRead() {
				return context.Canceled
			}
			if !index.HasDocument(sourceID) {
				source, at, exists, err := storage.ReadSourceEvidenceWithCaptureTime(ctx, reader, sourceID)
				if err != nil {
					return err
				}
				if exists {
					if err := s.indexSource(ctx, index, epoch, identity, source, at); err != nil {
						return err
					}
				}
			}
			s.mu.Lock()
			if s.epoch != epoch {
				s.mu.Unlock()
				return context.Canceled
			}
			delete(s.pending, sourceID)
			s.mu.Unlock()
			s.publishCounts(index, epoch, "indexing", false)
		}
	}
	_, captures := s.sources.(storage.CaptureEvidencePageReader)
	if capturesDirty && !fullScan {
		checkpoint.CaptureComplete = false
	}
	captureComplete := !captures || checkpoint.CaptureComplete
	if found && checkpoint.Complete && captureComplete && !fullScan {
		s.publishCounts(index, epoch, "ready", true)
		return nil
	}
	if !found || checkpoint.Complete && (captureComplete || forceLegacyRepair) {
		checkpoint = Checkpoint{ModelKey: identity.Key()}
		// A full repair is admitted durably before source processing. Its
		// continuation must not reset the legacy pass at every capture chunk.
		if fullScan {
			if err := s.repository.PutSemanticCheckpoint(ctx, checkpoint); err != nil {
				return err
			}
		}
		s.mu.Lock()
		s.repairAfterPass = false // This fresh pass already covers all older notifications.
		s.mu.Unlock()
	}
	fullScanConsumed = fullScan
	if fullScan && repairAdmitted != nil {
		*repairAdmitted = true
	}
	s.publishCounts(index, epoch, "indexing", false)
	if checkpoint.Complete {
		return s.backfillCaptures(ctx, index, epoch, identity, &checkpoint)
	}
	processed := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !s.canRead() {
			return ErrUnavailable
		}
		page, err := s.sources.ListObservationPage(ctx, storage.ObservationPageRequest{After: checkpoint.After, Limit: 4, MaxBytes: storage.MaxObservationPageBytes})
		if err != nil {
			return err
		}
		if len(page.CapturedAt) != len(page.Observations) || len(page.Observations) == 0 && page.HasMore {
			return ErrInvalid
		}
		for number, observation := range page.Observations {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !s.current(epoch) || !s.canRead() {
				return context.Canceled
			}
			if !index.HasDocument(observation.ContentID()) {
				source, err := capture.ObservationEvidence(observation)
				if err != nil {
					return ErrInvalid
				}
				if err := s.indexSource(ctx, index, epoch, identity, source, page.CapturedAt[number]); err != nil {
					return err
				}
			}
			checkpoint.After = observation.ContentID()
			processed++
			indexed, skipped, _ := index.Counts()
			checkpoint.Indexed, checkpoint.Skipped = uint64(indexed), uint64(skipped)
			if err := s.repository.PutSemanticCheckpoint(ctx, checkpoint); err != nil {
				return err
			}
			s.publishCounts(index, epoch, "indexing", false)
		}
		if !page.HasMore {
			s.mu.Lock()
			repair := s.repairAfterPass
			s.repairAfterPass = false
			s.mu.Unlock()
			if repair {
				// A dropped notification may sort before the current durable
				// cursor. Finish this pass, then persist a fresh repair cursor
				// before reporting complete coverage.
				checkpoint.After = ""
				checkpoint.Complete = false
				checkpoint.CaptureAfter, checkpoint.CaptureComplete = "", false
				if err := s.repository.PutSemanticCheckpoint(ctx, checkpoint); err != nil {
					return err
				}
				return errMoreWork
			}
			checkpoint.Complete = true
			if err := s.repository.PutSemanticCheckpoint(ctx, checkpoint); err != nil {
				return err
			}
			return s.backfillCaptures(ctx, index, epoch, identity, &checkpoint)
		}
		if processed >= 64 {
			return errMoreWork
		}
	}
}

func (s *Service) indexSource(ctx context.Context, index *Index, epoch uint64, identity Model, source capture.Evidence, at time.Time) error {
	if reader, ok := s.sources.(interface {
		IsCurrentNoteSource(context.Context, string) (bool, error)
	}); ok {
		current, err := reader.IsCurrentNoteSource(ctx, source.SourceID())
		if err != nil {
			return err
		}
		if !current {
			return nil
		}
	}
	document, err := s.buildCapture(ctx, identity, source, at)
	if err != nil {
		return err
	}
	defer clearDocument(document)
	if !s.current(epoch) || !s.canRead() || ctx.Err() != nil {
		return context.Canceled
	}
	if err := s.repository.PutSemanticDocument(ctx, document); err != nil {
		if errors.Is(err, ErrSourceGone) {
			return nil
		}
		return err
	}
	return index.AddDocument(ctx, document)
}

func (s *Service) buildCapture(ctx context.Context, identity Model, source capture.Evidence, at time.Time) (Document, error) {
	if source.Validate() != nil {
		return Document{}, ErrInvalid
	}
	text := source.Text()
	document := Document{SourceID: source.SourceID(), AppID: source.AppID(), Domain: source.Domain(), CapturedAt: at, TextBytes: len(text), Model: identity}
	if document.AppID == appidentity.BundleID {
		document.SkipReason = SkipSelf
		return document, nil
	}
	spans, err := SplitText(text)
	if err != nil {
		return Document{}, err
	}
	if len(spans) == 0 {
		document.SkipReason = SkipEmpty
		return document, nil
	}
	document.Chunks = make([]Chunk, 0, len(spans))
	for first := 0; first < len(spans); {
		budget, err := s.backgroundBudget(ctx)
		if err != nil {
			clearDocument(document)
			return Document{}, err
		}
		var input []string
		used := 0
		for next := first; next < len(spans) && len(input) < 8; next++ {
			span := spans[next]
			part := text[span.Start:span.End]
			cost := len(documentEmbeddingText(identity.Format, part))
			if used+cost > budget.MaxBackgroundTokens {
				break
			}
			used += cost
			input = append(input, part)
		}
		if len(input) == 0 || !budget.AllowBackgroundAI(power.LocalProvider, used, budget.MaxBackgroundDuration) {
			clearDocument(document)
			return Document{}, ErrPolicy
		}
		batchCtx, cancel := context.WithTimeout(ctx, budget.MaxBackgroundDuration)
		vectors, err := s.embedder.EmbedDocuments(batchCtx, identity, input)
		cancel()
		if err != nil {
			clearDocument(document)
			return Document{}, err
		}
		if len(vectors) != len(input) {
			clearDocument(document)
			return Document{}, ErrInvalid
		}
		for index, vector := range vectors {
			if !validVector(vector, identity.Dimensions) {
				clearDocument(document)
				return Document{}, ErrInvalid
			}
			span := spans[first+index]
			document.Chunks = append(document.Chunks, Chunk{Start: span.Start, End: span.End, Vector: vector})
		}
		first += len(input)
	}
	return document, nil
}

func (s *Service) Search(parent context.Context, question string, query Query) ([]Hit, error) {
	if s == nil || parent == nil || strings.TrimSpace(question) == "" || len(question) > 512 || !utf8.ValidString(question) || strings.ContainsRune(question, 0) {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	s.mu.Lock()
	if s.closed || s.index == nil {
		s.mu.Unlock()
		return nil, ErrUnavailable
	}
	index, identity, epoch := s.index, s.identity, s.epoch
	s.queryID++
	id := s.queryID
	s.queries[id] = cancel
	s.queryWG.Add(1)
	s.mu.Unlock()
	defer s.queryWG.Done()
	defer func() { s.mu.Lock(); delete(s.queries, id); s.mu.Unlock() }()
	if !s.canRead() {
		return nil, ErrUnavailable
	}
	vector, err := s.embedder.EmbedQuery(ctx, identity, question)
	if err != nil {
		s.Wake()
		return nil, err
	}
	defer clear(vector)
	query.Vector = vector
	if !query.Start.IsZero() {
		if resolver, ok := s.sources.(interface {
			SourceEvidenceSightingAt(context.Context, string, time.Time, time.Time) (time.Time, bool, error)
		}); ok {
			query.resolveCapturedAt = resolver.SourceEvidenceSightingAt
		} else if resolver, ok := s.sources.(interface {
			ObservationSightingAt(context.Context, string, time.Time, time.Time) (time.Time, bool, error)
		}); ok {
			query.resolveCapturedAt = resolver.ObservationSightingAt
		}
	}
	hits, err := index.Search(ctx, query)
	if err != nil {
		return nil, err
	}
	if reader, ok := s.sources.(interface {
		IsCurrentNoteSource(context.Context, string) (bool, error)
	}); ok {
		currentHits := hits[:0]
		for _, hit := range hits {
			current, err := reader.IsCurrentNoteSource(ctx, hit.SourceID)
			if err != nil {
				return nil, err
			}
			if current {
				currentHits = append(currentHits, hit)
			}
		}
		hits = currentHits
	}
	if classifier, ok := s.sources.(interface{ IsObservationTimeKnown(time.Time) bool }); ok {
		for i := range hits {
			if !classifier.IsObservationTimeKnown(hits[i].CapturedAt) {
				hits[i].CapturedAt = time.Time{}
			}
		}
	}
	if !s.current(epoch) || !s.canRead() {
		return nil, ErrUnavailable
	}
	return hits, nil
}

// RankCandidates returns cosine scores in the same order as texts. It does not
// sort or choose a relevance threshold. Blank candidates have score -1. Longer
// candidates use their best overlapping passage, matching index retrieval.
// Text and temporary vectors stay in this request; no candidate is persisted.
func (s *Service) RankCandidates(parent context.Context, question string, texts []string) ([]float32, error) {
	if s == nil || parent == nil || strings.TrimSpace(question) == "" || len(question) > 512 || !utf8.ValidString(question) || strings.ContainsRune(question, 0) || len(texts) > MaxRankCandidates {
		return nil, ErrInvalid
	}
	used := 0
	for _, text := range texts {
		if len(text) > MaxRankCandidateBytes || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			return nil, ErrInvalid
		}
		used += len(text)
		if used > MaxRankInputBytes {
			return nil, ErrInvalid
		}
	}
	if err := parent.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, maxRankDuration)
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	s.mu.Lock()
	if s.closed || s.index == nil || s.identity.Validate() != nil {
		s.mu.Unlock()
		return nil, ErrUnavailable
	}
	index, identity, epoch := s.index, s.identity, s.epoch
	s.queryID++
	id := s.queryID
	s.queries[id] = cancel
	s.queryWG.Add(1)
	s.mu.Unlock()
	defer s.queryWG.Done()
	defer func() { s.mu.Lock(); delete(s.queries, id); s.mu.Unlock() }()
	current := func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return !s.closed && s.epoch == epoch && s.index == index && s.identity.Key() == identity.Key()
	}
	if !s.canRead() {
		return nil, ErrUnavailable
	}
	scores := make([]float32, len(texts))
	for i := range scores {
		scores[i] = -1
	}
	if len(texts) == 0 {
		return scores, nil
	}
	vector, err := s.embedder.EmbedQuery(ctx, identity, question)
	defer clear(vector)
	if err != nil {
		s.Wake()
		return nil, err
	}
	if !validVector(vector, identity.Dimensions) {
		return nil, ErrInvalid
	}
	input, owners := make([]string, 0, 8), make([]int, 0, 8)
	defer func() { clear(input[:cap(input)]) }()
	flush := func() error {
		if len(input) == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !current() || !s.canRead() {
			return ErrUnavailable
		}
		vectors, err := s.embedder.EmbedDocuments(ctx, identity, input)
		defer func() {
			for _, item := range vectors {
				clear(item)
			}
		}()
		if err != nil {
			s.Wake()
			return err
		}
		if len(vectors) != len(input) {
			return ErrInvalid
		}
		for n, candidate := range vectors {
			if !validVector(candidate, identity.Dimensions) {
				return ErrInvalid
			}
			var score float64
			for dimension, value := range candidate {
				score += float64(value) * float64(vector[dimension])
			}
			scores[owners[n]] = max(scores[owners[n]], float32(max(-1, min(1, score))))
		}
		clear(input)
		input, owners = input[:0], owners[:0]
		return nil
	}
	used = 0
	for owner, text := range texts {
		spans, err := SplitText(text)
		if err != nil {
			return nil, err
		}
		for _, span := range spans {
			part := text[span.Start:span.End]
			cost := len(documentEmbeddingText(identity.Format, part))
			if len(input) == cap(input) || used+cost > model.MaxEmbeddingInputBytes {
				if err := flush(); err != nil {
					return nil, err
				}
				used = 0
			}
			input, owners = append(input, part), append(owners, owner)
			used += cost
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !current() || !s.canRead() {
		return nil, ErrUnavailable
	}
	return scores, nil
}

func (s *Service) current(epoch uint64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.closed && s.epoch == epoch
}

func (s *Service) canRead() bool { return s.options.CanRead == nil || s.options.CanRead() }

func (s *Service) publishCounts(index *Index, epoch uint64, state string, complete bool) {
	indexed, skipped, chunks := index.Counts()
	s.mu.Lock()
	if s.epoch == epoch && !s.closed {
		if complete && (len(s.pending) > 0 || s.repairAfterPass || s.capturesDirty) {
			state, complete = "indexing", false
		}
		s.status = Status{State: state, Model: s.identity.Name, IndexedSources: indexed, SkippedSources: skipped, Chunks: chunks, Complete: complete}
	}
	s.mu.Unlock()
	s.changed()
}

func (s *Service) changed() {
	if s.options.OnChange != nil {
		s.options.OnChange()
	}
}

func (s *Service) backgroundBudget(ctx context.Context) (power.Effective, error) {
	if s.options.Policy != nil {
		return s.options.Policy.Effective(ctx)
	}
	return power.Resolve(power.Config{Mode: power.Balanced}, power.SystemState{})
}

func errorCode(err error) string {
	switch {
	case errors.Is(err, model.ErrOllamaModelMissing):
		return "model_missing"
	case errors.Is(err, model.ErrOllamaRemoteModel):
		return "local_model_required"
	case errors.Is(err, model.ErrOllamaNotEmbedding):
		return "embedding_model_required"
	case errors.Is(err, model.ErrOllamaModelChanged), errors.Is(err, ErrModelMismatch):
		return "model_changed"
	case errors.Is(err, ErrPolicy):
		return "power_policy"
	case errors.Is(err, ErrUnavailable):
		return "memory_changing"
	default:
		return "index_unavailable"
	}
}
