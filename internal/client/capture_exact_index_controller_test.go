package client

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type captureOnlyExactIndex struct {
	finished chan struct{}
	calls    atomic.Int32
}

func (*captureOnlyExactIndex) ExactIndexPending(context.Context) (bool, error) { return false, nil }
func (*captureOnlyExactIndex) IndexPendingExact(context.Context, int) (bool, error) {
	panic("legacy work is empty")
}
func (s *captureOnlyExactIndex) CaptureIndexPending(context.Context) (bool, error) {
	return s.calls.Load() < 2, nil
}
func (s *captureOnlyExactIndex) IndexPendingCaptures(ctx context.Context, max int) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if max != 32 {
		panic("unbounded capture quantum")
	}
	return s.calls.Add(1) < 2, nil
}

func TestExactIndexControllerDrainsPublishedCapturesAndNotifiesOnlyWhenReady(t *testing.T) {
	s := &captureOnlyExactIndex{finished: make(chan struct{}, 1)}
	controller := newExactIndexController(s, nil, nil, func() { s.finished <- struct{}{} })
	defer controller.Close()
	select {
	case <-s.finished:
		if s.calls.Load() != 2 {
			t.Fatal("capture index readiness published before all quanta", s.calls.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("production index worker ignored published capture work")
	}
	if controller.Failed() {
		t.Fatal("successful capture indexing marked unavailable")
	}
}
