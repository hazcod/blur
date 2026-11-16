package model

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestApplePriorityPreemptsBackgroundButWaitsForRetirement(t *testing.T) {
	gate := &applePriorityGate{}
	running, retire, err := gate.acquire(WithBackgroundInference(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan func(), 1)
	go func() {
		_, release, err := gate.acquire(context.Background())
		if err == nil {
			acquired <- release
		}
	}()
	select {
	case <-running.Done():
	case <-time.After(time.Second):
		t.Fatal("interactive request did not preempt background")
	}
	if !errors.Is(context.Cause(running), ErrBackgroundPreempted) {
		t.Fatal("preemption indistinguishable from deletion", context.Cause(running))
	}
	select {
	case release := <-acquired:
		release()
		t.Fatal("native slot released before actual background retirement")
	case <-time.After(20 * time.Millisecond):
	}
	retire()
	select {
	case release := <-acquired:
		release()
	case <-time.After(time.Second):
		t.Fatal("interactive request did not acquire retired slot")
	}
}
func TestApplePriorityProtectsInteractiveAndCanceledWaitersCannotLaunch(t *testing.T) {
	gate := &applePriorityGate{}
	running, retire, err := gate.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, release, err := gate.acquire(ctx)
		if err == nil {
			release()
		}
		done <- err
	}()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("waiting cancellation lost", err)
	}
	select {
	case <-running.Done():
		t.Fatal("interactive request was preempted")
	default:
	}
	retire()
	_, release, err := gate.acquire(WithBackgroundInference(context.Background()))
	if err != nil {
		t.Fatal("canceled priority count pinned scheduler", err)
	}
	release()
}
func TestApplePriorityQueuedInteractiveBeatsBackground(t *testing.T) {
	gate := &applePriorityGate{}
	_, retire, err := gate.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	background := make(chan func(), 1)
	foreground := make(chan func(), 1)
	go func() {
		_, release, err := gate.acquire(WithBackgroundInference(context.Background()))
		if err == nil {
			background <- release
		}
	}()
	go func() {
		_, release, err := gate.acquire(context.Background())
		if err == nil {
			foreground <- release
		}
	}()
	deadline := time.Now().Add(time.Second)
	for {
		gate.mu.Lock()
		waiting := gate.interactiveWaiting
		gate.mu.Unlock()
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("interactive waiter not registered")
		}
		time.Sleep(time.Millisecond)
	}
	retire()
	select {
	case release := <-foreground:
		select {
		case r := <-background:
			r()
			t.Fatal("background overlapped interactive")
		default:
		}
		release()
	case <-time.After(time.Second):
		t.Fatal("foreground did not win")
	}
	select {
	case release := <-background:
		release()
	case <-time.After(time.Second):
		t.Fatal("deferred background did not resume")
	}
}
