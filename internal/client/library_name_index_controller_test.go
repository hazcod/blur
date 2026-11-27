package client

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type libraryOnlyExactIndex struct {
	calls     atomic.Int32
	completed chan struct{}
}

func (*libraryOnlyExactIndex) ExactIndexPending(context.Context) (bool, error) { return false, nil }
func (*libraryOnlyExactIndex) IndexPendingExact(context.Context, int) (bool, error) {
	panic("no source index work")
}
func (s *libraryOnlyExactIndex) LibraryNameIndexPending(context.Context) (bool, error) {
	return s.calls.Load() < 2, nil
}
func (s *libraryOnlyExactIndex) IndexPendingLibraryNames(ctx context.Context, max int) (bool, error) {
	if ctx.Err() != nil {
		return true, ctx.Err()
	}
	if max != 32 {
		panic("unbounded Library index quantum")
	}
	if s.calls.Add(1) == 2 {
		close(s.completed)
		return false, nil
	}
	return true, nil
}

func TestExactIndexControllerDrainsLibraryNamesWithoutModels(t *testing.T) {
	s := &libraryOnlyExactIndex{completed: make(chan struct{})}
	controller := newExactIndexController(s, nil, nil)
	defer controller.Close()
	select {
	case <-s.completed:
	case <-time.After(3 * time.Second):
		t.Fatal("local index worker ignored Library name backfill")
	}
	if controller.Failed() {
		t.Fatal("successful Library index marked unavailable")
	}
}
