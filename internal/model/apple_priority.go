package model

import (
	"context"
	"errors"
	"sync"
)

// ErrBackgroundPreempted defers optional Apple work for an interactive request.
// It is not a provider failure, user cancellation or permission to publish output.
var ErrBackgroundPreempted = errors.New("model: background inference deferred for interactive request")

type backgroundInferenceKey struct{}

// WithBackgroundInference marks optional work. Only Apple's local admission
// scheduler interprets this value; routing, consent and other providers do not.
func WithBackgroundInference(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, backgroundInferenceKey{}, true)
}

type applePriorityLease struct {
	background bool
	group      string
	retiring   bool
	cancel     context.CancelCauseFunc
}
type applePriorityGate struct {
	mu                 sync.Mutex
	changed            chan struct{}
	active             *applePriorityLease
	interactiveWaiting int
	backgroundWaiting  int
	waitingGroups      map[string]int
}

func (g *applePriorityGate) signalLocked() {
	if g.changed != nil {
		close(g.changed)
	}
	g.changed = make(chan struct{})
}

// acquire cancels background work but never retires its slot. Only the holder
// calls release AFTER its actual subprocess Run/Wait has returned.
func (g *applePriorityGate) acquire(ctx context.Context) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	background, _ := ctx.Value(backgroundInferenceKey{}).(bool)
	g.mu.Lock()
	if g.changed == nil {
		g.changed = make(chan struct{})
	}
	group, _ := ctx.Value(backgroundInferenceGroupKey{}).(string)
	if group == "" {
		group = "other"
	}
	if !background {
		g.interactiveWaiting++
	} else {
		g.backgroundWaiting++
		if g.waitingGroups == nil {
			g.waitingGroups = make(map[string]int)
		}
		g.waitingGroups[group]++
	}
	dequeue := func() {
		if !background {
			g.interactiveWaiting--
		} else {
			g.backgroundWaiting--
			g.waitingGroups[group]--
			if g.waitingGroups[group] == 0 {
				delete(g.waitingGroups, group)
			}
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			dequeue()
			g.signalLocked()
			g.mu.Unlock()
			return nil, nil, err
		}
		if g.active == nil && (!background || g.interactiveWaiting == 0) {
			dequeue()
			run, cancel := context.WithCancelCause(ctx)
			lease := &applePriorityLease{background: background, group: group, cancel: cancel}
			g.active = lease
			g.mu.Unlock()
			var once sync.Once
			release := func() {
				once.Do(func() {
					g.mu.Lock()
					if g.active == lease {
						g.active = nil
						g.signalLocked()
					}
					g.mu.Unlock()
					cancel(nil)
				})
			}
			return run, release, nil
		}
		if !background && g.active != nil && g.active.background {
			g.active.retiring = true
			g.active.cancel(ErrBackgroundPreempted)
		}
		changed := g.changed
		g.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		}
		g.mu.Lock()
	}
}
