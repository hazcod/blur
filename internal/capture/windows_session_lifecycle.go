package capture

import (
	"context"
	"sync"
)

func windowsUIACallWithAbandon[T any](ctx context.Context, call func() (T, error), discard func(T), abandoned func(T, error)) (T, bool, error) {
	var zero T
	if ctx == nil {
		return zero, false, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		if abandoned != nil {
			abandoned(zero, err)
		}
		return zero, false, err
	}
	result := make(chan struct {
		value T
		err   error
	})
	go func() {
		if err := ctx.Err(); err != nil {
			if abandoned != nil {
				abandoned(zero, err)
			}
			return
		}
		value, err := call()
		completed := struct {
			value T
			err   error
		}{value: value, err: err}
		select {
		case result <- completed:
		case <-ctx.Done():
			if discard != nil {
				discard(value)
			}
			if abandoned != nil {
				abandoned(value, err)
			}
		}
	}()
	select {
	case completed := <-result:
		return completed.value, true, completed.err
	case <-ctx.Done():
		return zero, false, ctx.Err()
	}
}

// windowsUIASessionGate bounds abandoned native automation sessions to one.
// A provider that never returns can therefore cost at most one worker/session.
type windowsUIASessionGate struct {
	slot chan struct{}
}

func newWindowsUIASessionGate() *windowsUIASessionGate {
	return &windowsUIASessionGate{slot: make(chan struct{}, 1)}
}

func (g *windowsUIASessionGate) acquire(ctx context.Context) (func(), error) {
	if g == nil || g.slot == nil || ctx == nil {
		return nil, context.Canceled
	}
	select {
	case g.slot <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-g.slot }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// windowsUIACleanupAsync lets capture shutdown finish even when a provider
// call is stuck. The slot remains held until all calls and native cleanup end.
func windowsUIACleanupAsync(pending *sync.WaitGroup, closeAutomation func(), releaseSlot func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		if pending != nil {
			pending.Wait()
		}
		if closeAutomation != nil {
			closeAutomation()
		}
		if releaseSlot != nil {
			releaseSlot()
		}
		close(done)
	}()
	return done
}
