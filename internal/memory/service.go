// Package memory coordinates bounded local persistence and materialization jobs.
package memory

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

var (
	ErrQueueFull  = errors.New("memory: writer queue full")
	ErrClosed     = errors.New("memory: service closed")
	ErrInvalidJob = errors.New("memory: invalid job")
)

type Job struct {
	// Key coalesces queued or running refreshes for the same materialized view.
	Key string
	Run func(context.Context) error
}

type Options struct{ QueueSize int }

type Stats struct{ Queued, Running, Completed, Failed, Coalesced, Rejected uint64 }

type Service struct {
	mu                                                      sync.Mutex
	queue                                                   chan Job
	root                                                    context.Context
	cancel                                                  context.CancelFunc
	closed                                                  bool
	keys                                                    map[string]struct{}
	done                                                    chan struct{}
	queued, running, completed, failed, coalesced, rejected atomic.Uint64
}

func New(options Options) *Service {
	if options.QueueSize < 1 {
		options.QueueSize = 16
	}
	if options.QueueSize > 1024 {
		options.QueueSize = 1024
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{queue: make(chan Job, options.QueueSize), root: ctx, cancel: cancel, keys: make(map[string]struct{}), done: make(chan struct{})}
	go s.run()
	return s
}

// Submit never blocks behind a stalled disk writer. A full queue is explicit;
// callers may retry after pressure drops. Non-empty keys coalesce refresh work.
func (s *Service) Submit(ctx context.Context, job Job) error {
	if ctx == nil || job.Run == nil || len(job.Key) > 128 {
		return ErrInvalidJob
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if job.Key != "" {
		if _, ok := s.keys[job.Key]; ok {
			s.coalesced.Add(1)
			return nil
		}
	}
	select {
	case s.queue <- job:
		s.queued.Add(1)
		if job.Key != "" {
			s.keys[job.Key] = struct{}{}
		}
		return nil
	default:
		s.rejected.Add(1)
		return ErrQueueFull
	}
}

func (s *Service) run() {
	defer close(s.done)
	for {
		select {
		case <-s.root.Done():
			s.discardPending()
			return
		case job := <-s.queue:
			s.queued.Add(^uint64(0))
			if s.root.Err() != nil {
				s.mu.Lock()
				if job.Key != "" {
					delete(s.keys, job.Key)
				}
				s.mu.Unlock()
				s.discardPending()
				return
			}
			s.running.Add(1)
			err := job.Run(s.root)
			s.running.Add(^uint64(0))
			s.mu.Lock()
			if job.Key != "" {
				delete(s.keys, job.Key)
			}
			s.mu.Unlock()
			if err != nil {
				s.failed.Add(1)
			} else {
				s.completed.Add(1)
			}
		}
	}
}

func (s *Service) discardPending() {
	for {
		select {
		case job := <-s.queue:
			s.queued.Add(^uint64(0))
			if job.Key != "" {
				s.mu.Lock()
				delete(s.keys, job.Key)
				s.mu.Unlock()
			}
		default:
			return
		}
	}
}

// Close cancels current work and waits for the worker to release its resources.
// Unfinished materializations must be scheduled again during startup recovery.
func (s *Service) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		return ErrInvalidJob
	}
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.cancel()
	}
	s.mu.Unlock()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	return Stats{Queued: s.queued.Load(), Running: s.running.Load(), Completed: s.completed.Load(), Failed: s.failed.Load(), Coalesced: s.coalesced.Load(), Rejected: s.rejected.Load()}
}
