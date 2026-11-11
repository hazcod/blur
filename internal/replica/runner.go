package replica

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// Interval is the fixed flush and import cadence.
const Interval = 10 * time.Minute

// Runner schedules Flush and Run. Passes are serialized; failures are logged
// content-free and retried on the next tick.
type Runner struct {
	Writer   *Writer
	Importer *Importer
	Interval time.Duration
	// OnPass, when set, is called after a background pass (Sync or FlushNow)
	// so the UI can refresh status such as the last backup time.
	OnPass func()

	mu     sync.Mutex // guards cancel/done/poke
	passMu sync.Mutex // serializes passes
	cancel context.CancelFunc
	done   chan struct{}
	poke   chan struct{}
}

// Start launches the background loop: one Import+Flush immediately, then every
// Interval. Calling Start twice is a no-op.
func (r *Runner) Start(parent context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	r.cancel, r.done, r.poke = cancel, make(chan struct{}), make(chan struct{}, 1)
	interval := r.Interval
	if interval <= 0 {
		interval = Interval
	}
	go func() {
		defer close(r.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		r.Sync(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.Sync(ctx)
			case <-r.poke:
				r.Sync(ctx)
			}
		}
	}()
}

// Poke asks the loop to run a pass soon; it never blocks.
func (r *Runner) Poke() {
	if r == nil {
		return
	}
	r.mu.Lock()
	poke := r.poke
	r.mu.Unlock()
	if poke == nil {
		return
	}
	select {
	case poke <- struct{}{}:
	default:
	}
}

// Sync runs Import then Flush once. Skipped quietly when sync is off.
func (r *Runner) Sync(ctx context.Context) (Result, error) {
	if r == nil {
		return Result{}, ErrDisabled
	}
	r.passMu.Lock()
	defer r.passMu.Unlock()
	var res Result
	var err error
	defer func() {
		if r.OnPass != nil {
			r.OnPass()
		}
	}()
	if r.Importer != nil {
		res, err = r.Importer.Run(ctx)
		if err != nil && !quiet(err) {
			slog.Warn("replica import failed", "result", "failed")
		}
	}
	if r.Writer != nil {
		if _, ferr := r.Writer.Flush(ctx); ferr != nil && !quiet(ferr) {
			slog.Warn("replica flush failed", "result", "failed")
			err = errors.Join(err, ferr)
		}
	}
	return res, err
}

// ImportNow runs one import pass only.
func (r *Runner) ImportNow(ctx context.Context) (Result, error) {
	if r == nil || r.Importer == nil {
		return Result{}, ErrDisabled
	}
	r.passMu.Lock()
	defer r.passMu.Unlock()
	return r.Importer.Run(ctx)
}

// FlushNow writes pending changes once (used at pause and quit).
func (r *Runner) FlushNow(ctx context.Context) error {
	if r == nil || r.Writer == nil {
		return nil
	}
	r.passMu.Lock()
	defer r.passMu.Unlock()
	_, err := r.Writer.Flush(ctx)
	if r.OnPass != nil {
		r.OnPass()
	}
	if quiet(err) {
		return nil
	}
	return err
}

// Close stops the loop and waits for it to exit.
func (r *Runner) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	cancel, done := r.cancel, r.done
	r.cancel = nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func quiet(err error) bool {
	return err == nil || errors.Is(err, ErrDisabled) || errors.Is(err, ErrMemoryChanging) || errors.Is(err, ErrNoKey) || errors.Is(err, ErrFolderNotReady)
}
