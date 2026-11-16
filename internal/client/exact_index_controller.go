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
				pending, err := store.ExactIndexPending(ctx)
				more := false
				if err == nil && pending {
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
			return pending || capturePending, err
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
