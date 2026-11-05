package lifecycle

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type testStore struct{ closed atomic.Bool }

func (s *testStore) Close() error { s.closed.Store(true); return nil }

func TestStartRecoveryAndCloseStore(t *testing.T) {
	store := &testStore{}
	var recovered atomic.Bool
	m := New(Options{QueueSize: 2, Store: store, Recover: func(context.Context) error { recovered.Store(true); return nil }})
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for !recovered.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if !store.closed.Load() || !recovered.Load() {
		t.Fatalf("store closed=%v recovered=%v", store.closed.Load(), recovered.Load())
	}
}
