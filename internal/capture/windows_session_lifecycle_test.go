package capture

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestWindowsUIASessionCleanupReturnsWhileProviderCallIsPending(t *testing.T) {
	gate := newWindowsUIASessionGate()
	releaseSlot, err := gate.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var pending sync.WaitGroup
	pending.Add(1)
	closed := make(chan struct{})
	returned := make(chan struct{})
	go func() {
		windowsUIACleanupAsync(&pending, func() { close(closed) }, releaseSlot)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("cleanup waited for the pending provider call")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := gate.acquire(ctx); err == nil {
		t.Fatal("a second UIA session acquired the slot while abandoned cleanup was pending")
	}
	select {
	case <-closed:
		t.Fatal("automation closed before its outstanding call completed")
	default:
	}

	pending.Done()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not finish after the provider call returned")
	}
	secondRelease, err := gate.acquire(context.Background())
	if err != nil {
		t.Fatalf("UIA slot was not released after cleanup: %v", err)
	}
	secondRelease()
}

func TestWindowsUIACallAbandonmentDefersCleanupUntilNativeCallReturns(t *testing.T) {
	started := make(chan struct{})
	finish := make(chan struct{})
	cleaned := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, transferred, err := windowsUIACallWithAbandon(ctx, func() (int, error) {
			close(started)
			<-finish
			return 7, nil
		}, func(value int) {
			if value != 7 {
				t.Errorf("discarded value = %d, want 7", value)
			}
		}, func(int, error) { close(cleaned) })
		if transferred {
			result <- errors.New("canceled provider result transferred to caller")
			return
		}
		result <- err
	}()
	<-started
	cancel()
	if err := <-result; err == nil {
		t.Fatal("canceled call returned nil error")
	}
	select {
	case <-cleaned:
		t.Fatal("abandon callback ran before the native call returned")
	default:
	}
	close(finish)
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("abandon callback did not run after native call returned")
	}
}
