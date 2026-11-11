package client

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
)

const maxChronicleFeedQueue = 32

type capturedEvidence struct {
	observation capture.SanitizedObservation
	at          time.Time
}

// chronicleFeed is an event-fed, memory-bounded bridge. It retains only
// explicitly opted-in sanitized evidence until the next observed hour; it
// neither polls nor reads other applications or stored history.
type chronicleFeed struct {
	mu      sync.Mutex
	worker  *chronicle.Worker
	current *chronicle.Hour
	queue   chan capturedEvidence
	timer   *time.Timer
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
}

func newChronicleFeed() *chronicleFeed {
	ctx, cancel := context.WithCancel(context.Background())
	f := &chronicleFeed{queue: make(chan capturedEvidence, maxChronicleFeedQueue), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	go f.run()
	return f
}

// Purge discards all pending and in-progress evidence. It runs at the start of
// every memory deletion window so deleted sources are never summarized later.
func (f *chronicleFeed) Purge() {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.current = nil
	if f.timer != nil {
		f.timer.Stop()
		f.timer = nil
	}
	f.drainQueueLocked()
	f.mu.Unlock()
}

// CurrentHour returns a deep copy of the in-progress hour's evidence, or false
// when nothing is being collected (no worker, purged, or paused). The copy is
// bounded by chronicle.MaxEvidence and shares no memory with the feed.
func (f *chronicleFeed) CurrentHour() (chronicle.Hour, bool) {
	if f == nil {
		return chronicle.Hour{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.current == nil || f.worker == nil {
		return chronicle.Hour{}, false
	}
	n := min(len(f.current.Evidence), chronicle.MaxEvidence)
	hour := chronicle.Hour{Start: f.current.Start, End: f.current.End, Evidence: make([]chronicle.Evidence, n)}
	for i := 0; i < n; i++ {
		item := f.current.Evidence[i]
		item.Observation = item.Observation.Clone()
		hour.Evidence[i] = item
	}
	return hour, true
}

func (f *chronicleFeed) drainQueueLocked() {
	for {
		select {
		case <-f.queue:
		default:
			return
		}
	}
}

func (f *chronicleFeed) SetWorker(w *chronicle.Worker) {
	if f == nil {
		return
	}
	f.mu.Lock()
	if f.worker != w {
		f.current = nil
		if f.timer != nil {
			f.timer.Stop()
			f.timer = nil
		}
		f.drainQueueLocked()
	}
	f.worker = w
	f.mu.Unlock()
}
func (f *chronicleFeed) Add(observation capture.SanitizedObservation, at time.Time) {
	if f == nil || observation.Validate() != nil || len(observation.Text()) > chronicle.MaxEvidenceItemBytes || at.IsZero() {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.worker == nil {
		return
	}
	select {
	case f.queue <- capturedEvidence{observation: observation.Clone(), at: at.UTC()}:
	default:
		slog.Debug("chronicle evidence skipped", "result", "bounded_queue_full")
	}
}
func (f *chronicleFeed) run() {
	defer close(f.done)
	for {
		select {
		case <-f.ctx.Done():
			return
		case item := <-f.queue:
			f.accept(item)
		}
	}
}
func (f *chronicleFeed) accept(item capturedEvidence) {
	at := item.at.UTC()
	start := at.Truncate(time.Hour)
	f.mu.Lock()
	worker := f.worker
	if worker == nil {
		f.current = nil
		f.mu.Unlock()
		return
	}
	if f.current == nil {
		f.current = &chronicle.Hour{Start: start, End: start.Add(time.Hour)}
		f.appendEvidence(item)
		f.startBoundaryTimerLocked()
		f.mu.Unlock()
		return
	}
	current := f.current
	if at.Before(current.Start) {
		f.mu.Unlock()
		return
	}
	if !at.Before(current.End) {
		closed := *current
		if f.timer != nil {
			f.timer.Stop()
			f.timer = nil
		}
		f.current = &chronicle.Hour{Start: start, End: start.Add(time.Hour)}
		f.appendEvidence(item)
		f.startBoundaryTimerLocked()
		f.mu.Unlock()
		f.schedule(worker, closed)
		return
	}
	f.appendEvidence(item)
	f.mu.Unlock()
}

func (f *chronicleFeed) startBoundaryTimerLocked() {
	if f.current == nil {
		return
	}
	wait := time.Until(f.current.End)
	if wait < 0 {
		wait = 0
	}
	f.timer = time.AfterFunc(wait, func() { f.closeCurrent(time.Now().UTC()) })
}

func (f *chronicleFeed) closeCurrent(now time.Time) {
	f.mu.Lock()
	if f.current == nil || now.Before(f.current.End) {
		f.mu.Unlock()
		return
	}
	hour := *f.current
	worker := f.worker
	f.current = nil
	f.timer = nil
	f.mu.Unlock()
	f.schedule(worker, hour)
}

func (f *chronicleFeed) schedule(worker *chronicle.Worker, hour chronicle.Hour) {
	if worker == nil || len(hour.Evidence) == 0 {
		return
	}
	if err := worker.Schedule(f.ctx, hour); err != nil {
		slog.Debug("closed chronicle hour skipped", "result", "not_scheduled")
	}
}
func (f *chronicleFeed) appendEvidence(item capturedEvidence) {
	if f.current == nil || len(f.current.Evidence) >= chronicle.MaxEvidence {
		return
	}
	for _, existing := range f.current.Evidence {
		if existing.Observation.ContentID() == item.observation.ContentID() {
			return
		}
	}
	f.current.Evidence = append(f.current.Evidence, chronicle.Evidence{Observation: item.observation, At: item.at, Salience: chronicleSalience(item.observation)})
}
func chronicleSalience(observation capture.SanitizedObservation) uint8 {
	value := len(observation.Text()) / 128
	if value > 8 {
		value = 8
	}
	return uint8(1 + value)
}
func (f *chronicleFeed) Close() {
	if f == nil {
		return
	}
	f.cancel()
	<-f.done
	f.mu.Lock()
	if f.timer != nil {
		f.timer.Stop()
		f.timer = nil
	}
	f.current = nil
	f.worker = nil
	f.mu.Unlock()
}

// sourceVerifiedRepository re-checks, immediately before persisting, that every
// source a summary cites still exists, and holds the memory-mutation fence
// through the write. Summaries citing deleted sources, or generated while a
// deletion window opened, are dropped.
type sourceVerifiedRepository struct {
	inner   chronicle.SummaryRepository
	sources interface {
		GetObservation(context.Context, string) (capture.SanitizedObservation, bool, error)
	}
	client *Client
}

func (r sourceVerifiedRepository) GetChronicle(ctx context.Context, id string) (chronicle.Summary, bool, error) {
	return r.inner.GetChronicle(ctx, id)
}

func (r sourceVerifiedRepository) PutChronicle(ctx context.Context, summary chronicle.Summary) error {
	epoch, stable := r.client.BeginMemoryRead()
	if !stable {
		return ErrMemoryChanging
	}
	for _, id := range summary.SourceIDs {
		_, found, err := r.sources.GetObservation(ctx, id)
		if err != nil {
			return err
		}
		if !found {
			slog.Debug("chronicle dropped", "result", "source_gone")
			return ErrMemoryChanging
		}
	}
	var err error
	if !r.client.PublishMemoryRead(epoch, func() { err = r.inner.PutChronicle(ctx, summary) }) {
		return ErrMemoryChanging
	}
	return err
}
