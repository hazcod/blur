// Package lifecycle owns startup recovery, bounded jobs, and orderly store close.
package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/nielsgroen/blur/internal/memory"
)

var ErrStarted = errors.New("lifecycle: already started")

type Store interface{ Close() error }
type JobFunc func(context.Context) error
type Options struct {
	QueueSize        int
	Store            Store
	Recover          JobFunc
	MaterializeToday JobFunc
	QuarantineSource func(context.Context, string) error
	Logger           *slog.Logger
}

type Manager struct {
	mu              sync.Mutex
	service         *memory.Service
	store           Store
	recover         JobFunc
	today           JobFunc
	quarantine      func(context.Context, string) error
	logger          *slog.Logger
	started, closed bool
	storeClosed     bool
	storeCloseErr   error
}

func New(options Options) *Manager {
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{service: memory.New(memory.Options{QueueSize: options.QueueSize}), store: options.Store, recover: options.Recover, today: options.MaterializeToday, quarantine: options.QuarantineSource, logger: logger}
}

func (m *Manager) Start(ctx context.Context) error {
	if ctx == nil {
		return memory.ErrInvalidJob
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return memory.ErrClosed
	}
	if m.started {
		return ErrStarted
	}
	m.started = true
	if m.recover != nil {
		return m.submit(ctx, "startup-recovery", m.recover)
	}
	return nil
}

func (m *Manager) Submit(ctx context.Context, key string, fn JobFunc) error {
	return m.submit(ctx, key, fn)
}
func (m *Manager) RefreshToday(ctx context.Context) error {
	if m.today == nil {
		return nil
	}
	return m.submit(ctx, "today-materialization", m.today)
}
func (m *Manager) QuarantineSource(ctx context.Context, sourceID string) error {
	if sourceID == "" || len(sourceID) > 128 || m.quarantine == nil {
		return memory.ErrInvalidJob
	}
	digest := sha256.Sum256([]byte(sourceID))
	key := "quarantine:" + hex.EncodeToString(digest[:])
	return m.submit(ctx, key, func(jobCtx context.Context) error { return m.quarantine(jobCtx, sourceID) })
}

func (m *Manager) submit(ctx context.Context, key string, fn JobFunc) error {
	if fn == nil {
		return memory.ErrInvalidJob
	}
	return m.service.Submit(ctx, memory.Job{Key: key, Run: func(jobCtx context.Context) error {
		start := time.Now()
		err := fn(jobCtx)
		code := "ok"
		if err != nil {
			code = "failed"
		}
		m.logger.Info("memory job finished", "operation", safeOperation(key), "duration_ms", time.Since(start).Milliseconds(), "result", code)
		return err
	}})
}

func safeOperation(key string) string {
	switch key {
	case "startup-recovery":
		return key
	case "today-materialization":
		return key
	case "":
		return "source-quarantine"
	default:
		if len(key) > 11 && key[:11] == "quarantine:" {
			return "source-quarantine"
		}
		return "write"
	}
}

func (m *Manager) Stats() memory.Stats { return m.service.Stats() }

// Close cancels in-flight work, waits for the worker, then closes the store.
// The durable store recovers committed transactions on the next open; callers
// should run startup recovery again to rebuild interrupted materializations.
func (m *Manager) Close(ctx context.Context) error {
	if ctx == nil {
		return memory.ErrInvalidJob
	}
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	queueErr := m.service.Close(ctx)
	if queueErr != nil {
		return queueErr
	}
	m.mu.Lock()
	if !m.storeClosed {
		m.storeClosed = true
		if m.store != nil {
			m.storeCloseErr = m.store.Close()
		}
	}
	storeErr := m.storeCloseErr
	m.mu.Unlock()
	return errors.Join(storeErr)
}
