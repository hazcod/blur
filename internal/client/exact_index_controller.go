package client

import (
	"context"
	"errors"
	"sync"
	"time"
)

type exactIndexStore interface {
	IndexPendingExact(context.Context, int) (bool, error)
	ExactIndexPending(context.Context) (bool, error)
}

type captureExactIndexStore interface {
	IndexPendingCaptures(context.Context, int) (bool, error)
	CaptureIndexPending(context.Context) (bool, error)
}

type libraryNameIndexStore interface {
	IndexPendingLibraryNames(context.Context, int) (bool, error)
	LibraryNameIndexPending(context.Context) (bool, error)
}

type chronicleErasureMaintenanceStore interface {
	ResumeChronicleErasure(context.Context) (worked, more bool, err error)
}

// Exact indexing is a local storage service. It runs even when all AI models
// and automatic Library enrichment are disabled.
type exactIndexController struct {
	cancel context.CancelFunc
	done   chan struct{}
	wake   chan struct{}
	once   sync.Once
	mu     sync.RWMutex
	failed bool
}

func newExactIndexController(store exactIndexStore, canRead func() bool, onChange func(), onCapturesReady ...func()) *exactIndexController {
	ctx, cancel := context.WithCancel(context.Background())
	c := &exactIndexController{cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1)}
	go func() {
		defer close(c.done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			if canRead == nil || canRead() {
				pending, more := false, false
				var err error
				if recaps, ok := store.(chronicleErasureMaintenanceStore); ok {
					pending, more, err = recaps.ResumeChronicleErasure(ctx)
				}
				// Finish a pending privacy operation before ordinary index work.
				// This service exists even when every AI feature is disabled.
				if err == nil && !more {
					exactPending, exactErr := store.ExactIndexPending(ctx)
					pending, err = pending || exactPending, exactErr
					if err == nil && exactPending {
						more, err = store.IndexPendingExact(ctx, 32)
					}
					if captures, ok := store.(captureExactIndexStore); ok && ctx.Err() == nil {
						capturePending, captureErr := captures.CaptureIndexPending(ctx)
						captureMore := false
						if captureErr == nil && capturePending {
							captureMore, captureErr = captures.IndexPendingCaptures(ctx, 32)
						}
						pending, more = pending || capturePending, more || captureMore
						err = errors.Join(err, captureErr)
						if capturePending && captureErr == nil && !captureMore && ctx.Err() == nil {
							for _, ready := range onCapturesReady {
								if ready != nil {
									ready()
								}
							}
						}
					}
					if library, ok := store.(libraryNameIndexStore); ok && ctx.Err() == nil {
						libraryPending, libraryErr := library.LibraryNameIndexPending(ctx)
						libraryMore := false
						if libraryErr == nil && libraryPending {
							libraryMore, libraryErr = library.IndexPendingLibraryNames(ctx, 32)
						}
						pending, more = pending || libraryPending, more || libraryMore
						err = errors.Join(err, libraryErr)
					}
				}
				if ctx.Err() != nil {
					return
				}
				c.mu.Lock()
				changed := c.failed != (err != nil)
				c.failed = err != nil
				c.mu.Unlock()
				if onChange != nil && (changed || err == nil && pending) {
					onChange()
				}
				if err == nil && more {
					continue
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-c.wake:
			}
		}
	}()
	return c
}

func (c *exactIndexController) Wake() {
	if c == nil {
		return
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
}
func (c *exactIndexController) Close() {
	if c == nil {
		return
	}
	c.once.Do(func() { c.cancel(); <-c.done })
}

type exactCoverageReader interface {
	ExactIndexPending(context.Context) (bool, error)
}

func (c *Client) ExactIndexPending(ctx context.Context) (bool, error) {
	if c == nil || c.data == nil || ctx == nil {
		return false, ErrNotConfigured
	}
	if reader, ok := c.data.(exactCoverageReader); ok {
		pending, err := reader.ExactIndexPending(ctx)
		if err != nil {
			return false, err
		}
		if captures, ok := c.data.(captureExactIndexStore); ok {
			capturePending, err := captures.CaptureIndexPending(ctx)
			if err != nil {
				return false, err
			}
			pending = pending || capturePending
		}
		if library, ok := c.data.(libraryNameIndexStore); ok {
			libraryPending, err := library.LibraryNameIndexPending(ctx)
			return pending || libraryPending, err
		}
		return pending, nil
	}
	return false, nil
}

const pendingExactIndexCopy = "Some saved content was still being indexed when this search ran. Search again shortly for full coverage."

func (c *exactIndexController) Failed() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.failed
}
