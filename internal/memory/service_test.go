package memory

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestQueueBackpressureAndCoalescing(t *testing.T) {
	s := New(Options{QueueSize: 1})
	started := make(chan struct{})
	finish := make(chan struct{})
	if err := s.Submit(context.Background(), Job{Key: "first", Run: func(context.Context) error { close(started); <-finish; return nil }}); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := s.Submit(context.Background(), Job{Key: "today", Run: func(context.Context) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	if err := s.Submit(context.Background(), Job{Key: "today", Run: func(context.Context) error { return errors.New("must coalesce") }}); err != nil {
		t.Fatalf("duplicate should coalesce: %v", err)
	}
	if err := s.Submit(context.Background(), Job{Key: "overflow", Run: func(context.Context) error { return nil }}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("got %v, want backpressure", err)
	}
	close(finish)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if got := s.Stats().Coalesced; got != 1 {
		t.Fatalf("coalesced=%d", got)
	}
}

func TestSubmitCancellationAndClosed(t *testing.T) {
	s := New(Options{QueueSize: 1})
	block := make(chan struct{})
	started := make(chan struct{})
	_ = s.Submit(context.Background(), Job{Key: "run", Run: func(context.Context) error { close(started); <-block; return nil }})
	<-started
	_ = s.Submit(context.Background(), Job{Key: "queued", Run: func(context.Context) error { return nil }})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Submit(ctx, Job{Key: "cancelled", Run: func(context.Context) error { return nil }}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	close(block)
	closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
	defer closeCancel()
	if err := s.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	if err := s.Submit(context.Background(), Job{Key: "late", Run: func(context.Context) error { return nil }}); !errors.Is(err, ErrClosed) {
		t.Fatalf("got %v", err)
	}
}

func TestCloseCancelsRunningJob(t *testing.T) {
	s := New(Options{QueueSize: 1})
	started := make(chan struct{})
	var cancelled atomic.Bool
	_ = s.Submit(context.Background(), Job{Key: "cancel", Run: func(ctx context.Context) error { close(started); <-ctx.Done(); cancelled.Store(true); return ctx.Err() }})
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if !cancelled.Load() {
		t.Fatal("running job did not observe shutdown")
	}
}

func TestCloseDoesNotStartQueuedJobs(t *testing.T) {
	s := New(Options{QueueSize: 2})
	started := make(chan struct{})
	var queuedRan atomic.Bool
	_ = s.Submit(context.Background(), Job{Key: "running", Run: func(ctx context.Context) error { close(started); <-ctx.Done(); return ctx.Err() }})
	<-started
	_ = s.Submit(context.Background(), Job{Key: "queued", Run: func(context.Context) error { queuedRan.Store(true); return nil }})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if queuedRan.Load() {
		t.Fatal("queued job ran after shutdown")
	}
	if got := s.Stats().Queued; got != 0 {
		t.Fatalf("queued count after shutdown=%d", got)
	}
}
